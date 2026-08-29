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

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
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

func TestLegacyAPIArtifactIgnoresEarlierRequestScopedArtifact(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 30, 0, 0, time.UTC)
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Username: "user-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}
	for _, requestState := range []domain.SourceArtifactState{domain.SourceArtifactReady, domain.SourceArtifactPending} {
		t.Run(string(requestState), func(t *testing.T) {
			database, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.AutoMigrate(&repositories.TenantRecord{}, &repositories.UserRecord{}, &repositories.SourceArtifactRecord{}, &repositories.SourceArtifactRequestRecord{}, &repositories.DataMountBindingRecord{}); err != nil {
				t.Fatal(err)
			}
			repo := repositories.NewGormRepository(database)
			if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
				t.Fatal(err)
			}
			requested, err := domain.NewRequestScopedSourceArtifact(domain.SourceArtifactInput{
				ID: "artifact-0123456789abcdef01234567", TenantID: principal.TenantID, UserID: principal.Subject,
				SHA256: apiArtifactDigest, SizeBytes: 7,
			}, now.Add(15*time.Minute), now)
			if err != nil {
				t.Fatal(err)
			}
			storedRequest, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), &requested, "source-request-0123456789abcdef01234567", repositories.DefaultSourceArtifactLimits())
			if err != nil {
				t.Fatal(err)
			}
			if requestState == domain.SourceArtifactReady {
				if _, err := repo.MarkSourceArtifactReady(context.Background(), principal.TenantID, principal.Subject, storedRequest.ID, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			store := newImmutableTOSMock(t)
			requestPayload := []byte("request")
			legacyPayload := []byte("legacy!")
			store.seed(storedRequest.ObjectKey, apiArtifactDigest, requestPayload)
			router := artifactTestRouter(t, repo, store, principal, now.Add(2*time.Minute))
			body := `{"sha256":"` + apiArtifactDigest + `","sizeBytes":7}`
			created := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", body)
			if created.Code != http.StatusCreated {
				t.Fatalf("legacy API create after %s request status=%d body=%s", requestState, created.Code, created.Body.String())
			}
			var envelope struct {
				Data sourceArtifactResponse `json:"data"`
			}
			if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			legacy, err := repo.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, envelope.Data.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			wantKey, err := domain.SourceArtifactObjectKey(principal.TenantID, principal.Subject, apiArtifactDigest)
			if err != nil {
				t.Fatal(err)
			}
			if legacy.ObjectKey != wantKey || legacy.ID == storedRequest.ID {
				t.Fatalf("legacy API artifact mismatch: request=%q/%q legacy=%q/%q want=%q", storedRequest.ID, storedRequest.ObjectKey, legacy.ID, legacy.ObjectKey, wantKey)
			}
			upload, err := http.NewRequest(http.MethodPut, envelope.Data.UploadURL, bytes.NewReader(legacyPayload))
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range envelope.Data.RequiredHeaders {
				upload.Header.Set(key, value)
			}
			uploadResponse, err := store.server.Client().Do(upload)
			if err != nil {
				t.Fatal(err)
			}
			uploadResponse.Body.Close()
			if uploadResponse.StatusCode != http.StatusOK {
				t.Fatalf("legacy immutable upload after %s request status=%d", requestState, uploadResponse.StatusCode)
			}
			completed := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts/"+legacy.ID+"/complete", "")
			if completed.Code != http.StatusOK {
				t.Fatalf("legacy complete after %s request status=%d body=%s", requestState, completed.Code, completed.Body.String())
			}
			if !bytes.Equal(store.object(storedRequest.ObjectKey), requestPayload) || !bytes.Equal(store.object(legacy.ObjectKey), legacyPayload) {
				t.Fatalf("request or legacy immutable object changed: request=%q legacy=%q", store.object(storedRequest.ObjectKey), store.object(legacy.ObjectKey))
			}
			retry := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", body)
			if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), `"artifactId":"`+legacy.ID+`"`) {
				t.Fatalf("legacy API retry did not reuse artifact: status=%d body=%s", retry.Code, retry.Body.String())
			}
		})
	}
}
