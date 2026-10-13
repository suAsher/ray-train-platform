package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMediaTOSClient struct {
	fakeTOSClient
	headKey          string
	request          tosMediaReadRequest
	info             MediaInfo
	response         tosMediaReadResponse
	headErr, readErr error
}

func (s *fakeMediaTOSClient) HeadMedia(_ context.Context, _, key string) (MediaInfo, error) {
	s.headKey = key
	return s.info, s.headErr
}
func (s *fakeMediaTOSClient) ReadMedia(_ context.Context, r tosMediaReadRequest) (tosMediaReadResponse, error) {
	s.request = r
	return s.response, s.readErr
}

func TestTOSMediaScopedRangeAndVersion(t *testing.T) {
	client := &fakeMediaTOSClient{info: MediaInfo{SizeBytes: 10, ETag: "\"v1\""}, response: tosMediaReadResponse{Content: io.NopCloser(strings.NewReader("345")), SizeBytes: 3, ETag: "\"v1\"", ContentRange: "bytes 3-5/10"}}
	s, err := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "test", Bucket: "private", AccessKey: "ak", SecretKey: "sk"}, client)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.HeadMedia(context.Background(), "personal/user/files/", "nested/a.mp4")
	if err != nil || client.headKey != "personal/user/files/nested/a.mp4" {
		t.Fatalf("head %v %s", err, client.headKey)
	}
	r, err := s.ReadMediaRange(context.Background(), "personal/user/files/", "nested/a.mp4", info, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Content.Close()
	if client.request.Key != client.headKey || client.request.Range != "bytes=3-5" || client.request.ETag != "\"v1\"" {
		t.Fatalf("unbounded/unversioned request %#v", client.request)
	}
	if _, err := s.HeadMedia(context.Background(), "personal/user/files/", "../other/a.mp4"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := s.ReadMediaRange(context.Background(), "personal/user/files/", "a.mp4", info, 9, 10); err == nil {
		t.Fatal("out of bounds accepted")
	}
}

func TestTOSMediaRejectsIgnoredRangeAndChangedVersion(t *testing.T) {
	for _, tc := range []struct {
		length   int64
		cr, etag string
	}{{10, "", "\"v1\""}, {3, "bytes 0-2/10", "\"v1\""}, {3, "bytes 3-5/10", "\"v2\""}} {
		closed := false
		client := &fakeMediaTOSClient{response: tosMediaReadResponse{Content: &mediaCloseTest{Reader: strings.NewReader("345"), closed: &closed}, SizeBytes: tc.length, ContentRange: tc.cr, ETag: tc.etag}}
		s, _ := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "test", Bucket: "private", AccessKey: "ak", SecretKey: "sk"}, client)
		if _, err := s.ReadMediaRange(context.Background(), "personal/user/files", "a.mp4", MediaInfo{SizeBytes: 10, ETag: "\"v1\""}, 3, 5); err == nil || !closed {
			t.Fatalf("invalid provider response accepted or unclosed: %v %v", err, closed)
		}
	}
}

type mediaCloseTest struct {
	io.Reader
	closed *bool
}

func (r *mediaCloseTest) Close() error { *r.closed = true; return nil }

func TestTOSMediaSDKSendsRangeAndIfMatch(t *testing.T) {
	gets := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "personal/u/a.mp4") {
			t.Errorf("wrong object path %s", r.URL.Path)
		}
		w.Header().Set("ETag", "\"version1\"")
		w.Header().Set("Last-Modified", "Mon, 12 Oct 2026 01:00:00 GMT")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10000000000")
			return
		}
		gets++
		if r.Header.Get("Range") != "bytes=9999999997-9999999999" || r.Header.Get("If-Match") != "\"version1\"" {
			t.Errorf("unbounded or unversioned request: %v", r.Header)
		}
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Range", "bytes 9999999997-9999999999/10000000000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("end"))
	}))
	defer server.Close()
	s, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "test", Bucket: "private", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.HeadMedia(context.Background(), "personal/u", "a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ReadMediaRange(context.Background(), "personal/u", "a.mp4", info, info.SizeBytes-3, info.SizeBytes-1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Content.Close()
	body, err := io.ReadAll(r.Content)
	if err != nil || string(body) != "end" || gets != 1 {
		t.Fatalf("body=%s gets=%d err=%v", body, gets, err)
	}
}

func TestTOSMediaSDKRejectsSymlinkAndSanitizesErrors(t *testing.T) {
	for _, tc := range []struct {
		code       int
		objectType string
		want       error
	}{{200, "Symlink", ErrUnavailable}, {404, "", ErrNotFound}, {412, "", ErrMediaChanged}, {403, "", ErrUnavailable}} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "10")
			w.Header().Set("ETag", "\"v1\"")
			w.Header().Set("X-Tos-Object-Type", tc.objectType)
			w.WriteHeader(tc.code)
		}))
		s, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "test", Bucket: "private", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = s.HeadMedia(context.Background(), "personal/u", "a.mp4")
		server.Close()
		if err != tc.want {
			t.Fatalf("status %d type %s err=%v", tc.code, tc.objectType, err)
		}
	}
}

func TestTOSMediaFailuresAndValidators(t *testing.T) {
	config := TOSConfig{Endpoint: "https://tos.example.com", Region: "test", Bucket: "private", AccessKey: "ak", SecretKey: "sk"}
	client := &fakeMediaTOSClient{info: MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
	s, _ := newTOSStoreWithClient(config, client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.HeadMedia(ctx, "r", "a.mp4"); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := s.ReadMediaRange(ctx, "r", "a.mp4", client.info, 0, 1); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := s.ReadMediaRange(context.Background(), "r", "../a.mp4", client.info, 0, 1); err == nil {
		t.Fatal("traversal")
	}
	client.headErr = ErrNotFound
	if _, err := s.HeadMedia(context.Background(), "r", "a.mp4"); err != ErrNotFound {
		t.Fatal(err)
	}
	client.headErr = nil
	client.info.ETag = "bad"
	if _, err := s.HeadMedia(context.Background(), "r", "a.mp4"); err != ErrUnavailable {
		t.Fatal(err)
	}
	closed := false
	client.readErr = ErrMediaChanged
	client.response.Content = &mediaCloseTest{Reader: strings.NewReader("x"), closed: &closed}
	if _, err := s.ReadMediaRange(context.Background(), "r", "a.mp4", MediaInfo{SizeBytes: 10, ETag: "\"v1\""}, 0, 1); err != ErrMediaChanged || !closed {
		t.Fatalf("err=%v closed=%v", err, closed)
	}
	s, _ = newTOSStoreWithClient(config, &fakeTOSClient{})
	if _, err := s.HeadMedia(context.Background(), "r", "a.mp4"); err != ErrUnavailable {
		t.Fatal(err)
	}
	if _, err := s.ReadMediaRange(context.Background(), "r", "a.mp4", MediaInfo{SizeBytes: 10, ETag: "\"v1\""}, 0, 1); err != ErrUnavailable {
		t.Fatal(err)
	}
	for _, v := range []string{"", "bad", "\"a b\"", "\"a\"b\"", "\"a\x7f\""} {
		if validMediaETag(v) {
			t.Fatalf("bad tag %q", v)
		}
	}
	if mediaETag("raw") != "\"raw\"" || mediaETag("\"v1\"") != "\"v1\"" || mediaETag("bad\n") != "" {
		t.Fatal("unsafe tag normalization")
	}
}

func TestTOSMediaSDKReadFailureResponses(t *testing.T) {
	for _, tc := range []struct {
		code       int
		objectType string
		want       error
	}{{200, "", ErrUnavailable}, {206, "Symlink", ErrUnavailable}, {404, "", ErrNotFound}, {412, "", ErrMediaChanged}, {403, "", ErrUnavailable}} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "3")
			w.Header().Set("ETag", "\"v1\"")
			w.Header().Set("X-Tos-Object-Type", tc.objectType)
			w.Header().Set("Content-Range", "bytes 0-2/10")
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte("abc"))
		}))
		s, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "test", Bucket: "private", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = s.ReadMediaRange(context.Background(), "personal/u", "a.mp4", MediaInfo{SizeBytes: 10, ETag: "\"v1\""}, 0, 2)
		server.Close()
		if err != tc.want {
			t.Fatalf("code=%d type=%s err=%v", tc.code, tc.objectType, err)
		}
	}
}
