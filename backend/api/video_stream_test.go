package api

import (
	"context"
	"github.com/gin-gonic/gin"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type fakeVideoStore struct {
	fakeDataSpaceObjectStore
	fakeArtifactReader
	root, file string
	start, end int64
	reads      int
	closed     bool
	info       objectstore.MediaInfo
	err        error
	readFn     func(objectstore.MediaInfo, int64, int64) (objectstore.ArtifactRead, error)
}

func (s *fakeVideoStore) HeadMedia(_ context.Context, root, file string) (objectstore.MediaInfo, error) {
	s.root, s.file = root, file
	return s.info, s.err
}
func (s *fakeVideoStore) ReadMediaRange(_ context.Context, root, file string, info objectstore.MediaInfo, start, end int64) (objectstore.ArtifactRead, error) {
	s.reads++
	s.start, s.end = start, end
	if s.readFn != nil {
		return s.readFn(info, start, end)
	}
	content := "0123456789"[int(start):int(end+1)]
	return objectstore.ArtifactRead{Content: &videoTestBody{Reader: strings.NewReader(content), closed: &s.closed}, SizeBytes: end - start + 1, ETag: info.ETag}, nil
}

type videoTestBody struct {
	io.Reader
	closed *bool
}

func (b *videoTestBody) Close() error { *b.closed = true; return nil }

func TestVideoDataSpaceRangesAndHead(t *testing.T) {
	for _, tc := range []struct {
		method, r, ifRange string
		code               int
		body, cr           string
		start, end         int64
		reads              int
	}{
		{"GET", "", "", 200, "0123456789", "", 0, 9, 1},
		{"GET", "bytes=2-5", "", 206, "2345", "bytes 2-5/10", 2, 5, 1},
		{"GET", "bytes=7-", "", 206, "789", "bytes 7-9/10", 7, 9, 1},
		{"GET", "bytes=-3", "", 206, "789", "bytes 7-9/10", 7, 9, 1},
		{"GET", "bytes=2-99", "", 206, "23456789", "bytes 2-9/10", 2, 9, 1},
		{"GET", "bytes=2-5", "\"old\"", 200, "0123456789", "", 0, 9, 1},
		{"GET", "bytes=2-5", "\"v1\"", 206, "2345", "bytes 2-5/10", 2, 5, 1},
		{"HEAD", "bytes=2-5", "", 200, "", "", 0, 0, 0},
	} {
		t.Run(tc.method+tc.r+tc.ifRange, func(t *testing.T) {
			s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
			h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
			router := dataSpaceRouter(h, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
			req := httptest.NewRequest(tc.method, "/api/v1/data-spaces/my-files/video?path=dir/test.mp4", nil)
			req.Header.Set("Range", tc.r)
			req.Header.Set("If-Range", tc.ifRange)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.code || w.Body.String() != tc.body || w.Header().Get("Content-Range") != tc.cr {
				t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			if s.reads != tc.reads || s.start != tc.start || s.end != tc.end || s.reads > 0 && !s.closed {
				t.Fatalf("unsafe read: %#v", s)
			}
			if s.root != "ray-train/tenants/tenant-a/users/user-a/files/" || s.file != "dir/test.mp4" {
				t.Fatalf("unscoped read: %#v", s)
			}
			if w.Header().Get("Accept-Ranges") != "bytes" || w.Header().Get("Content-Type") != "video/mp4" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("ETag") != "\"v1\"" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestVideoRangeRejectsInvalidOrMultipleRanges(t *testing.T) {
	for _, r := range []string{"bytes=10-", "bytes=5-2", "bytes=-0", "bytes=0-1,4-5", "bytes=9223372036854775808-", "items=0-1", "bytes=a-", "bytes=+1-2", "bytes=--1"} {
		s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
		h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
		router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
		req := httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.webm", nil)
		req.Header.Set("Range", r)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 416 || w.Header().Get("Content-Range") != "bytes */10" || s.reads != 0 {
			t.Fatalf("%s: %d %v %#v", r, w.Code, w.Header(), s)
		}
	}
}

func TestVideoDataSpaceRejectsUnsupportedAndTraversalBeforeStorage(t *testing.T) {
	for _, file := range []string{"a.txt", "a.mp4/../secret", "%2Fother%2Fa.mp4", "../other.mp4", ""} {
		s := &fakeVideoStore{}
		h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
		router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path="+file, nil))
		if w.Code != 400 && w.Code != 415 || s.root != "" {
			t.Fatalf("%s: %d %#v", file, w.Code, s)
		}
	}
}

func TestVideoJobRejectsOtherTenantBeforeStorage(t *testing.T) {
	s := &fakeVideoStore{}
	h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{artifactJob("b", "tenant-b")}}, Options{ArtifactReader: s})
	router := artifactRouter(h, auth.Principal{Subject: "a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/jobs/b/artifacts/video?path=a.mp4", nil))
	if w.Code != 404 || s.root != "" {
		t.Fatalf("%d %#v", w.Code, s)
	}
}

func TestVideoLargeSeekOnlyReadsRequestedBytes(t *testing.T) {
	const size int64 = 8 * 1024 * 1024 * 1024
	s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: size, ETag: "\"v1\""}}
	s.readFn = func(info objectstore.MediaInfo, start, end int64) (objectstore.ArtifactRead, error) {
		if start != size-3 || end != size-1 {
			t.Fatalf("unexpected fetch offset %d-%d", start, end)
		}
		return objectstore.ArtifactRead{Content: io.NopCloser(strings.NewReader("end")), SizeBytes: 3, ETag: info.ETag}, nil
	}
	h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
	router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
	req := httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.mp4", nil)
	req.Header.Set("Range", "bytes=-3")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 206 || w.Body.String() != "end" || w.Header().Get("Content-Length") != "3" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

type blockingVideoBody struct {
	entered, closed chan struct{}
	once            sync.Once
}

func (b *blockingVideoBody) Read(_ []byte) (int, error) {
	close(b.entered)
	<-b.closed
	return 0, io.EOF
}
func (b *blockingVideoBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestVideoCancellationClosesUpstreamPromptly(t *testing.T) {
	b := &blockingVideoBody{entered: make(chan struct{}), closed: make(chan struct{})}
	s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
	s.readFn = func(info objectstore.MediaInfo, _, _ int64) (objectstore.ArtifactRead, error) {
		return objectstore.ArtifactRead{Content: b, SizeBytes: 10, ETag: info.ETag}, nil
	}
	h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
	router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.mp4", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { router.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("did not enter upstream read")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not close upstream body")
	}
}

func TestVideoJobOwnOutputAndForeignPersonalBoundary(t *testing.T) {
	s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
	job := artifactJob("job-a", "tenant-a")
	assets := &fakeStorageAssetStore{assets: []domain.StorageAsset{{ID: "outputs", Name: "outputs", Kind: domain.StorageAssetOutput, Provider: domain.StorageProviderTOS, ClaimName: "tos-outputs", RootPrefix: "platform/tenant-a/outputs", BrowseEnabled: true}}}
	h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{StorageAssets: assets, ArtifactReader: s})
	router := artifactRouter(h, auth.Principal{Subject: "a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("HEAD", "/api/v1/jobs/job-a/artifacts/video?path=a.webm", nil))
	if w.Code != 200 || w.Body.Len() != 0 || s.reads != 0 || s.root != "platform/tenant-a/outputs/runs/job-a" {
		t.Fatalf("%d %v %#v", w.Code, w.Header(), s)
	}
	job.UserID = "other"
	job.Spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace, ClaimName: "other", SubPath: "runs/job-a", MountPath: domain.DataMountOutputPath}
	s.root = ""
	h = NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{ArtifactReader: s})
	router = artifactRouter(h, auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/jobs/job-a/artifacts/video?path=a.mp4", nil))
	if w.Code != 403 || s.root != "" {
		t.Fatalf("foreign personal output %d %#v", w.Code, s)
	}
}

func TestVideoReadErrorsAreSanitizedAndCapabilityOptional(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{objectstore.ErrNotFound, 404}, {objectstore.ErrMediaChanged, 412}, {objectstore.ErrUnavailable, 502}} {
		s := &fakeVideoStore{err: tc.err}
		h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
		router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.mp4", nil))
		if w.Code != tc.code || strings.Contains(w.Body.String(), "ray-train/") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: &fakeDataSpaceObjectStore{}})
	router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.mp4", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestVideoAuthenticationAndPATScope(t *testing.T) {
	s := &fakeVideoStore{}
	h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s, ArtifactReader: s})
	router := gin.New()
	h.RegisterDataSpaceRoutes(router.Group("/api/v1"))
	h.RegisterTrainingRoutes(router.Group("/api/v1"))
	for _, u := range []string{"/api/v1/data-spaces/my-files/video?path=a.mp4", "/api/v1/jobs/a/artifacts/video?path=a.mp4"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", u, nil))
		if w.Code != 401 || s.root != "" {
			t.Fatalf("anonymous %d %#v", w.Code, s)
		}
	}
	router = artifactRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}})
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/jobs/a/artifacts/video?path=a.mp4", nil))
		if w.Code != 403 || s.root != "" {
			t.Fatalf("PAT %d %#v", w.Code, s)
		}
	}
}

func TestVideoSharedSpaceAllowedButIDCNotBrowsable(t *testing.T) {
	for _, tc := range []struct {
		id, root string
		code     int
	}{{"team-shared", "ray-train/tenants/tenant-a/shared/", 200}, {"idc-original", "", 409}, {"unknown-space", "", 404}} {
		s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
		h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
		router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("HEAD", "/api/v1/data-spaces/"+tc.id+"/video?path=a.mp4", nil))
		if w.Code != tc.code || s.root != tc.root {
			t.Fatalf("%s status=%d root=%s", tc.id, w.Code, s.root)
		}
	}
}

func TestVideoGetErrorsAndInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		err  error
		read objectstore.ArtifactRead
		code int
	}{{objectstore.ErrMediaChanged, objectstore.ArtifactRead{}, 412}, {objectstore.ErrNotFound, objectstore.ArtifactRead{}, 404}, {nil, objectstore.ArtifactRead{SizeBytes: 10, ETag: "\"v1\""}, 502}, {nil, objectstore.ArtifactRead{Content: io.NopCloser(strings.NewReader("bad")), SizeBytes: 3, ETag: "\"v2\""}, 502}} {
		s := &fakeVideoStore{info: objectstore.MediaInfo{SizeBytes: 10, ETag: "\"v1\""}}
		s.readFn = func(objectstore.MediaInfo, int64, int64) (objectstore.ArtifactRead, error) { return tc.read, tc.err }
		h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
		router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.mp4", nil))
		if w.Code != tc.code {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	for _, info := range []objectstore.MediaInfo{{SizeBytes: -1, ETag: "\"v1\""}, {SizeBytes: 10, ETag: "evil\r\nheader"}, {SizeBytes: 0, ETag: "\"v1\""}} {
		s := &fakeVideoStore{info: info}
		h := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: s})
		router := dataSpaceRouter(h, auth.Principal{Subject: "a", TenantID: "t", AuthType: auth.AuthTypeLocal})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/data-spaces/my-files/video?path=a.mp4", nil))
		want := 502
		if info.SizeBytes == 0 {
			want = 200
		}
		if w.Code != want || s.reads != 0 {
			t.Fatalf("%d %#v", w.Code, s)
		}
	}
}

func TestVideoRangeValidatorDatesAndBoundaries(t *testing.T) {
	modified := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		r, ir       string
		size        int64
		partial, ok bool
	}{{"bytes=-999", "", 10, true, true}, {"bytes=0-0", "", 10, true, true}, {"bytes=-", "", 10, false, false}, {"bytes=0-1", "", 0, false, false}, {"bytes=", "", 10, false, false}, {"bytes=0-1", modified.Format("Mon, 02 Jan 2006 15:04:05 GMT"), 10, true, true}, {"bytes=0-1", modified.Add(-time.Hour).Format("Mon, 02 Jan 2006 15:04:05 GMT"), 10, false, true}, {"bytes=0-1", "W/\"v1\"", 10, false, true}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Range", tc.r)
		r.Header.Set("If-Range", tc.ir)
		_, _, partial, ok := videoRange(r, objectstore.MediaInfo{SizeBytes: tc.size, ETag: "\"v1\"", LastModified: modified})
		if partial != tc.partial || ok != tc.ok {
			t.Fatalf("%#v: partial=%v ok=%v", tc, partial, ok)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Add("Range", "bytes=0-1")
	r.Header.Add("Range", "bytes=2-3")
	if _, _, _, ok := videoRange(r, objectstore.MediaInfo{SizeBytes: 10}); ok {
		t.Fatal("duplicate Range headers accepted")
	}
	for _, etag := range []string{"", "v1", "\"a b\"", "\"a\"b\"", "\"a\x7f\""} {
		if validVideoETag(etag) {
			t.Fatalf("invalid tag %q accepted", etag)
		}
	}
	if videoContentType("A.WEBM") != "video/webm" {
		t.Fatal("uppercase extension rejected")
	}
}
