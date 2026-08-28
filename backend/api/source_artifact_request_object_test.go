package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type immutableTOSMock struct {
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	digests map[string]string
}

func newImmutableTOSMock(t *testing.T) *immutableTOSMock {
	t.Helper()
	mock := &immutableTOSMock{objects: map[string][]byte{}, digests: map[string]string{}}
	mock.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		key := request.URL.Query().Get("key")
		if request.Method != http.MethodPut || key == "" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Header.Get("If-None-Match") != "*" || request.Header.Get("x-tos-forbid-overwrite") != "true" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		payload, err := io.ReadAll(io.LimitReader(request.Body, domain.MaxSourceArtifactSize+1))
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if _, exists := mock.objects[key]; exists {
			writer.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		mock.objects[key] = append([]byte(nil), payload...)
		mock.digests[key] = request.Header.Get("x-tos-meta-sha256")
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.server.Close)
	return mock
}

func (mock *immutableTOSMock) PresignPut(_ context.Context, key, digest string, size int64, ttl time.Duration) (objectstore.PresignedPut, error) {
	return objectstore.PresignedPut{
		URL: mock.server.URL + "/upload?key=" + url.QueryEscape(key), ContentLength: size, ExpiresAt: time.Now().Add(ttl),
		RequiredHeaders: map[string]string{
			"Content-Type": "application/zip", "x-tos-meta-sha256": digest,
			"If-None-Match": "*", "x-tos-forbid-overwrite": "true",
		},
	}, nil
}

func (mock *immutableTOSMock) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	payload, exists := mock.objects[key]
	if !exists {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return objectstore.ObjectInfo{SizeBytes: int64(len(payload)), Metadata: map[string]string{"sha256": mock.digests[key]}}, nil
}

func (mock *immutableTOSMock) Put(_ context.Context, key, digest string, size int64, body io.Reader) error {
	payload, err := io.ReadAll(io.LimitReader(body, domain.MaxSourceArtifactSize+1))
	if err != nil || int64(len(payload)) != size {
		return objectstore.ErrUnavailable
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if _, exists := mock.objects[key]; exists {
		return objectstore.ErrAlreadyExists
	}
	mock.objects[key] = append([]byte(nil), payload...)
	mock.digests[key] = digest
	return nil
}

func (mock *immutableTOSMock) seed(key, digest string, payload []byte) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	mock.objects[key] = append([]byte(nil), payload...)
	mock.digests[key] = digest
}

func (mock *immutableTOSMock) object(key string) []byte {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return append([]byte(nil), mock.objects[key]...)
}

func TestRequestScopedArtifactUploadsBesideExistingImmutableDigestObject(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}
	for _, state := range []domain.SourceArtifactState{domain.SourceArtifactReady, domain.SourceArtifactPending} {
		t.Run(string(state), func(t *testing.T) {
			legacy, err := domain.NewSourceArtifact(domain.SourceArtifactInput{ID: "artifact-legacy", TenantID: principal.TenantID, UserID: principal.Subject, SHA256: apiArtifactDigest, SizeBytes: 7}, now.Add(15*time.Minute), now)
			if err != nil {
				t.Fatal(err)
			}
			if state == domain.SourceArtifactReady {
				legacy, err = legacy.MarkReady(now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
			}
			store := newImmutableTOSMock(t)
			oldPayload := []byte("old-zip")
			newPayload := []byte("new-zip")
			store.seed(legacy.ObjectKey, apiArtifactDigest, oldPayload)
			repo := &fakeSourceArtifactRepository{artifact: &legacy}
			router := artifactTestRouter(t, repo, store, principal, now)
			requestID := "source-request-0123456789abcdef01234567"
			created := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", `{"clientRequestId":"`+requestID+`","sha256":"`+apiArtifactDigest+`","sizeBytes":7}`)
			if created.Code != http.StatusCreated {
				t.Fatalf("create request artifact status=%d body=%s", created.Code, created.Body.String())
			}
			var envelope struct {
				Data sourceArtifactResponse `json:"data"`
			}
			if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if repo.artifact == nil || repo.artifact.ObjectKey == legacy.ObjectKey || !strings.HasPrefix(repo.artifact.ObjectKey, legacy.StorageRoot+"workspace/.ray-train-archives/") {
				t.Fatalf("request artifact did not receive an owner-scoped unique key: legacy=%q request=%+v", legacy.ObjectKey, repo.artifact)
			}
			upload, err := http.NewRequest(http.MethodPut, envelope.Data.UploadURL, bytes.NewReader(newPayload))
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range envelope.Data.RequiredHeaders {
				upload.Header.Set(key, value)
			}
			response, err := store.server.Client().Do(upload)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("immutable TOS upload status=%d; key collided with legacy object", response.StatusCode)
			}
			completed := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts/"+envelope.Data.ArtifactID+"/complete", "")
			if completed.Code != http.StatusOK {
				t.Fatalf("complete request artifact status=%d body=%s", completed.Code, completed.Body.String())
			}
			if !bytes.Equal(store.object(legacy.ObjectKey), oldPayload) || !bytes.Equal(store.object(repo.artifact.ObjectKey), newPayload) {
				t.Fatalf("immutable objects changed unexpectedly: old=%q new=%q", store.object(legacy.ObjectKey), store.object(repo.artifact.ObjectKey))
			}
		})
	}
}
