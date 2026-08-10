package objectstore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestTOSStorePresignPutSignsRequiredHeadersForFifteenMinutes(t *testing.T) {
	now := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	store, err := NewTOSStore(TOSConfig{
		Endpoint: "https://tos-cn-beijing.volces.com", Region: "cn-beijing", Bucket: "private-bucket",
		AccessKey: "test-access-key", SecretKey: "test-secret-key", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new TOS store: %v", err)
	}
	result, err := store.PresignPut(context.Background(), "tenants/t/users/u/sha256/"+testDigest+".zip", testDigest, 1234, 15*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	if result.ExpiresAt != now.Add(15*time.Minute) {
		t.Fatalf("unexpected expiry: %s", result.ExpiresAt)
	}
	if result.ContentLength != 1234 {
		t.Fatalf("contentLength=%d, want 1234", result.ContentLength)
	}
	wantBrowserHeaders := map[string]string{
		"Content-Type":           "application/zip",
		"x-tos-meta-sha256":      testDigest,
		"If-None-Match":          "*",
		"x-tos-forbid-overwrite": "true",
	}
	for key, want := range wantBrowserHeaders {
		if result.RequiredHeaders[key] != want {
			t.Fatalf("required header %q=%q, want %q", key, result.RequiredHeaders[key], want)
		}
	}
	if _, ok := result.RequiredHeaders["Content-Length"]; ok {
		t.Fatal("browser contract must not ask JavaScript to set Content-Length")
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	signed := strings.ToLower(parsed.Query().Get("X-Tos-SignedHeaders"))
	for _, header := range []string{"content-length", "content-type", "x-tos-meta-sha256", "if-none-match", "x-tos-forbid-overwrite"} {
		if !strings.Contains(";"+signed+";", ";"+header+";") {
			t.Fatalf("header %q must be signed; signed=%q", header, signed)
		}
	}
}

func TestTOSStoreHeadReturnsLengthAndLowercaseMetadata(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Length", "4321")
		w.Header().Set("X-Tos-Meta-Sha256", testDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := NewTOSStore(TOSConfig{
		Endpoint: server.URL, Region: "cn-beijing", Bucket: "private-bucket",
		AccessKey: "test-access-key", SecretKey: "test-secret-key", Transport: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := store.Head(context.Background(), "tenants/t/users/u/sha256/"+testDigest+".zip")
	if err != nil {
		t.Fatalf("head object: %v", err)
	}
	if info.SizeBytes != 4321 || info.Metadata["sha256"] != testDigest {
		t.Fatalf("unexpected head info: size=%d metadata=%v", info.SizeBytes, info.Metadata)
	}
}

func TestTOSStoreHeadMapsNotFoundAndTransientWithoutLeakingSDKDetails(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "not found", status: http.StatusNotFound, want: ErrNotFound},
		{name: "unavailable", status: http.StatusServiceUnavailable, want: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Tos-Request-Id", "sensitive-request-id")
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			store, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Head(context.Background(), "safe-key")
			if !errors.Is(err, test.want) {
				t.Fatalf("head error=%v, want classification %v", err, test.want)
			}
			if strings.Contains(err.Error(), "sensitive-request-id") || strings.Contains(err.Error(), "safe-key") {
				t.Fatalf("object store error leaked internal details: %v", err)
			}
		})
	}
}

func TestNewTOSStoreRejectsIncompleteOrInsecureConfiguration(t *testing.T) {
	tests := []TOSConfig{
		{},
		{Endpoint: "http://tos.example", Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"},
		{Endpoint: "https://tos.example", Region: "", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"},
		{Endpoint: "https://tos.example", Region: "cn", Bucket: "", AccessKey: "ak", SecretKey: "sk"},
		{Endpoint: "https://tos.example", Region: "cn", Bucket: "bucket", AccessKey: "", SecretKey: "sk"},
	}
	for index, config := range tests {
		if _, err := NewTOSStore(config); err == nil {
			t.Fatalf("configuration %d should fail", index)
		}
	}
}
