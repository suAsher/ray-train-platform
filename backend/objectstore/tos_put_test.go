package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestTOSStorePutStreamsValidatedArchiveWithImmutableHeaders(t *testing.T) {
	payload := []byte("PK\\x03\\x04archive")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Fatalf("method=%s", request.Method)
		}
		if request.Header.Get("Content-Type") != "application/zip" || request.Header.Get("X-Tos-Forbid-Overwrite") != "true" || request.Header.Get("If-None-Match") != "*" || request.Header.Get("X-Tos-Meta-Sha256") != testDigest {
			t.Fatalf("missing immutable headers: %v", request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || !bytes.Equal(body, payload) {
			t.Fatalf("body=%q err=%v", body, err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "cn", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "ray-train/tenants/tenant/users/user/workspace/.ray-train-archives/"+testDigest+".zip", testDigest, int64(len(payload)), bytes.NewReader(payload)); err != nil {
		t.Fatalf("put: %v", err)
	}
}

func TestTOSStorePutClassifiesConflictsAndDoesNotLeakStorageDetails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Tos-Request-Id", "sensitive-request-id")
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	store, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "cn", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Put(context.Background(), "ray-train/tenants/tenant/users/user/workspace/.ray-train-archives/"+testDigest+".zip", testDigest, 1, strings.NewReader("x"))
	if !errors.Is(err, ErrAlreadyExists) || strings.Contains(err.Error(), "sensitive-request-id") {
		t.Fatalf("put error=%v", err)
	}
}

func TestTOSStorePutMapsPreconditionFailureToAlreadyExists(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer server.Close()
	store, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "cn", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "ray-train/tenants/tenant/users/user/workspace/.ray-train-archives/"+testDigest+".zip", testDigest, 1, strings.NewReader("x")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("412 put error=%v", err)
	}
}

func TestTOSStorePutKeepsLegacyDigestObjectAndRequestObjectImmutable(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	legacy, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: "artifact-legacy", TenantID: "tenant", UserID: "user", SHA256: testDigest, SizeBytes: 3,
	}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	requested, err := domain.NewRequestScopedSourceArtifact(domain.SourceArtifactInput{
		ID: "artifact-0123456789abcdef01234567", TenantID: "tenant", UserID: "user", SHA256: testDigest, SizeBytes: 3,
	}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string][]byte{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") != "*" || request.Header.Get("X-Tos-Forbid-Overwrite") != "true" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, exists := objects[request.URL.Path]; exists {
			writer.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		objects[request.URL.Path] = append([]byte(nil), body...)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := NewTOSStore(TOSConfig{Endpoint: server.URL, Region: "cn", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk", Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), legacy.ObjectKey, testDigest, 3, strings.NewReader("old")); err != nil {
		t.Fatalf("put legacy object: %v", err)
	}
	if err := store.Put(context.Background(), requested.ObjectKey, testDigest, 3, strings.NewReader("new")); err != nil {
		t.Fatalf("put request object beside legacy digest: %v", err)
	}
	if err := store.Put(context.Background(), legacy.ObjectKey, testDigest, 3, strings.NewReader("bad")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("legacy overwrite error=%v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("immutable TOS mock stored %d objects, want legacy plus request", len(objects))
	}
}
