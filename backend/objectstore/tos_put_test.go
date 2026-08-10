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
	if err := store.Put(context.Background(), "tenants/tenant/users/user/sha256/"+testDigest+".zip", testDigest, int64(len(payload)), bytes.NewReader(payload)); err != nil {
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
	err = store.Put(context.Background(), "tenants/tenant/users/user/sha256/"+testDigest+".zip", testDigest, 1, strings.NewReader("x"))
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
	if err := store.Put(context.Background(), "tenants/tenant/users/user/sha256/"+testDigest+".zip", testDigest, 1, strings.NewReader("x")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("412 put error=%v", err)
	}
}
