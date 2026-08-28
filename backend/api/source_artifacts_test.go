package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

const apiArtifactDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeSourceArtifactRepository struct {
	artifact     *domain.SourceArtifact
	identity     int
	markedReady  int
	identityErr  error
	createErr    error
	getErr       error
	markReadyErr error
	reopenErr    error
}

func (f *fakeSourceArtifactRepository) EnsureIdentity(context.Context, auth.Principal) error {
	f.identity++
	return f.identityErr
}

func (f *fakeSourceArtifactRepository) CreateOrReuseSourceArtifact(_ context.Context, artifact *domain.SourceArtifact) (*domain.SourceArtifact, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.artifact == nil {
		copy := *artifact
		f.artifact = &copy
	}
	copy := *f.artifact
	return &copy, nil
}

func (f *fakeSourceArtifactRepository) GetSourceArtifact(_ context.Context, tenant, user, id string) (*domain.SourceArtifact, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.artifact == nil || f.artifact.ID != id || f.artifact.TenantID != tenant || f.artifact.UserID != user {
		return nil, repositories.ErrSourceArtifactNotFound
	}
	copy := *f.artifact
	return &copy, nil
}

func (f *fakeSourceArtifactRepository) MarkSourceArtifactReady(_ context.Context, tenant, user, id string, completed time.Time) (*domain.SourceArtifact, error) {
	if f.markReadyErr != nil {
		return nil, f.markReadyErr
	}
	artifact, err := f.GetSourceArtifact(context.Background(), tenant, user, id)
	if err != nil {
		return nil, err
	}
	ready, err := artifact.MarkReady(completed)
	if err != nil {
		return nil, err
	}
	f.markedReady++
	f.artifact = &ready
	return &ready, nil
}

type fakeArtifactStore struct {
	presign       objectstore.PresignedPut
	presignErr    error
	presignCalls  int
	head          objectstore.ObjectInfo
	headErr       error
	headCalls     int
	lastObjectKey string
}

func (f *fakeArtifactStore) PresignPut(_ context.Context, key, _ string, _ int64, _ time.Duration) (objectstore.PresignedPut, error) {
	f.presignCalls++
	f.lastObjectKey = key
	return f.presign, f.presignErr
}

func (f *fakeArtifactStore) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	f.headCalls++
	f.lastObjectKey = key
	return f.head, f.headErr
}

func artifactPrincipalMiddleware(principal auth.Principal) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	}
}

func artifactTestRouter(t *testing.T, repo *fakeSourceArtifactRepository, store *fakeArtifactStore, principal auth.Principal, now time.Time) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler, err := NewSourceArtifactHandler(repo, store, SourceArtifactOptions{
		Now: func() time.Time { return now }, NewID: func() (string, error) { return "artifact-fixed", nil },
	})
	if err != nil {
		t.Fatalf("new artifact handler: %v", err)
	}
	router := gin.New()
	router.Use(artifactPrincipalMiddleware(principal))
	handler.RegisterRoutes(router.Group("/api/v1"))
	return router
}

func performArtifactRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "request-artifact")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestSourceArtifactPublicRegistrarRequiresSourceWriteScopeAndEngineerRole(t *testing.T) {
	now := time.Now().UTC()
	body := `{"sha256":"` + apiArtifactDigest + `","sizeBytes":100}`
	tests := []struct {
		name      string
		principal auth.Principal
	}{
		{name: "missing scope", principal: auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}},
		{name: "missing role", principal: auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Viewer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeSourceArtifactRepository{}
			response := performArtifactRequest(artifactTestRouter(t, repo, &fakeArtifactStore{}, test.principal, now), http.MethodPost, "/api/v1/source-artifacts", body)
			if response.Code != http.StatusForbidden || repo.identity != 0 {
				t.Fatalf("authorization failure expected: status=%d identity=%d", response.Code, repo.identity)
			}
		})
	}
}

func TestCreateSourceArtifactReturnsPresignWithoutCachingOrCredentialLeak(t *testing.T) {
	now := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	uploadURL := "https://private-bucket.tos.example/object?X-Tos-Signature=do-not-log"
	store := &fakeArtifactStore{presign: objectstore.PresignedPut{
		URL: uploadURL, ExpiresAt: now.Add(15 * time.Minute), ContentLength: 100,
		RequiredHeaders: map[string]string{
			"Content-Type": "application/zip", "x-tos-meta-sha256": apiArtifactDigest,
			"If-None-Match": "*", "x-tos-forbid-overwrite": "true",
		},
	}}
	repo := &fakeSourceArtifactRepository{}
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}
	response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, "/api/v1/source-artifacts", `{"sha256":"`+apiArtifactDigest+`","sizeBytes":100}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d", response.Code)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("presign response is cacheable: headers=%v", response.Header())
	}
	if repo.identity != 1 || store.presignCalls != 1 || store.lastObjectKey != "ray-train/tenants/tenant-a/users/user-a/workspace/.ray-train-archives/"+apiArtifactDigest+".zip" {
		t.Fatalf("unexpected create calls: identity=%d presign=%d key=%q", repo.identity, store.presignCalls, store.lastObjectKey)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			ArtifactID      string            `json:"artifactId"`
			State           string            `json:"state"`
			SHA256          string            `json:"sha256"`
			SizeBytes       int64             `json:"sizeBytes"`
			UploadURL       string            `json:"uploadUrl"`
			RequiredHeaders map[string]string `json:"requiredHeaders"`
			ContentLength   int64             `json:"contentLength"`
			ExpiresAt       time.Time         `json:"expiresAt"`
			UploadRequired  bool              `json:"uploadRequired"`
		} `json:"data"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ContentLength != 100 {
		t.Fatalf("contentLength=%d, want 100", envelope.Data.ContentLength)
	}
	if _, ok := envelope.Data.RequiredHeaders["Content-Length"]; ok {
		t.Fatal("browser contract unexpectedly contains Content-Length")
	}
	if !envelope.Success || envelope.Data.ArtifactID != "artifact-fixed" || envelope.Data.State != "PENDING" || envelope.Data.SHA256 != apiArtifactDigest || envelope.Data.SizeBytes != 100 || envelope.Data.UploadURL != uploadURL || !envelope.Data.UploadRequired || envelope.Data.ExpiresAt != now.Add(15*time.Minute) || envelope.RequestID != "request-artifact" {
		t.Fatalf("unexpected create response metadata: success=%t id=%q state=%q sha=%q size=%d upload=%t expires=%s request=%q", envelope.Success, envelope.Data.ArtifactID, envelope.Data.State, envelope.Data.SHA256, envelope.Data.SizeBytes, envelope.Data.UploadRequired, envelope.Data.ExpiresAt, envelope.RequestID)
	}
}

func TestCreateSourceArtifactHonorsValidatedCallerArtifactID(t *testing.T) {
	now := time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)
	const requestedID = "artifact-0123456789abcdef01234567"
	repo := &fakeSourceArtifactRepository{}
	store := &fakeArtifactStore{presign: objectstore.PresignedPut{
		URL: "https://objects.example/upload", ExpiresAt: now.Add(SourceArtifactUploadTTL), ContentLength: 100,
	}}
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response := performArtifactRequest(
		artifactTestRouter(t, repo, store, principal, now),
		http.MethodPost,
		"/api/v1/source-artifacts",
		`{"artifactId":"`+requestedID+`","sha256":"`+apiArtifactDigest+`","sizeBytes":100}`,
	)
	if response.Code != http.StatusCreated || repo.artifact == nil || repo.artifact.ID != requestedID || !strings.Contains(response.Body.String(), `"artifactId":"`+requestedID+`"`) {
		t.Fatalf("caller artifact identity was not preserved: status=%d artifact=%+v body=%s", response.Code, repo.artifact, response.Body.String())
	}
}

func TestCreateSourceArtifactRejectsUnsafeCallerArtifactIDBeforePersistence(t *testing.T) {
	now := time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)
	repo := &fakeSourceArtifactRepository{}
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response := performArtifactRequest(
		artifactTestRouter(t, repo, &fakeArtifactStore{}, principal, now),
		http.MethodPost,
		"/api/v1/source-artifacts",
		`{"artifactId":"artifact-../../foreign","sha256":"`+apiArtifactDigest+`","sizeBytes":100}`,
	)
	if response.Code != http.StatusBadRequest || repo.identity != 0 || repo.artifact != nil {
		t.Fatalf("unsafe artifact identity reached persistence: status=%d identity=%d artifact=%+v", response.Code, repo.identity, repo.artifact)
	}
}

func TestCreateSourceArtifactReadyReuseDoesNotIssueUploadURL(t *testing.T) {
	now := time.Now().UTC()
	ready, err := domain.NewSourceArtifact(domain.SourceArtifactInput{ID: "ready", TenantID: "tenant", UserID: "user", SHA256: apiArtifactDigest, SizeBytes: 100}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	ready, err = ready.MarkReady(now)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeSourceArtifactRepository{artifact: &ready}
	store := &fakeArtifactStore{head: objectstore.ObjectInfo{SizeBytes: 100, Metadata: map[string]string{"sha256": apiArtifactDigest}}}
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, "/api/v1/source-artifacts", `{"sha256":"`+apiArtifactDigest+`","sizeBytes":100}`)
	if response.Code != http.StatusOK || store.presignCalls != 0 || strings.Contains(response.Body.String(), "uploadUrl") || !strings.Contains(response.Body.String(), `"uploadRequired":false`) {
		t.Fatalf("ready reuse response invalid: status=%d presign=%d body=%s", response.Code, store.presignCalls, response.Body.String())
	}
}

func TestCompleteSourceArtifactValidatesHeadBeforeReady(t *testing.T) {
	now := time.Now().UTC()
	base, err := domain.NewSourceArtifact(domain.SourceArtifactInput{ID: "artifact", TenantID: "tenant", UserID: "user", SHA256: apiArtifactDigest, SizeBytes: 100}, now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		head      objectstore.ObjectInfo
		headErr   error
		wantCode  int
		wantRetry bool
	}{
		{name: "success", head: objectstore.ObjectInfo{SizeBytes: 100, Metadata: map[string]string{"sha256": apiArtifactDigest}}, wantCode: http.StatusOK},
		{name: "size mismatch", head: objectstore.ObjectInfo{SizeBytes: 99, Metadata: map[string]string{"sha256": apiArtifactDigest}}, wantCode: http.StatusConflict},
		{name: "metadata mismatch", head: objectstore.ObjectInfo{SizeBytes: 100, Metadata: map[string]string{"sha256": strings.Repeat("f", 64)}}, wantCode: http.StatusConflict},
		{name: "object missing", headErr: objectstore.ErrNotFound, wantCode: http.StatusNotFound},
		{name: "store unavailable", headErr: objectstore.ErrUnavailable, wantCode: http.StatusServiceUnavailable, wantRetry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := base
			repo := &fakeSourceArtifactRepository{artifact: &copy}
			store := &fakeArtifactStore{head: test.head, headErr: test.headErr}
			principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}
			response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, "/api/v1/source-artifacts/artifact/complete", `{}`)
			if response.Code != test.wantCode {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantCode, response.Body.String())
			}
			wantMarked := 0
			if test.wantCode == http.StatusOK {
				wantMarked = 1
			}
			if repo.markedReady != wantMarked {
				t.Fatalf("mark ready calls=%d want=%d", repo.markedReady, wantMarked)
			}
			if test.wantRetry && response.Header().Get("Retry-After") == "" {
				t.Fatal("transient TOS failure omitted Retry-After")
			}
			if strings.Contains(response.Body.String(), base.ObjectKey) || strings.Contains(response.Body.String(), "X-Tos-Signature") {
				t.Fatal("error response leaked object storage details")
			}
		})
	}
}

func TestCompleteSourceArtifactHidesCrossOwnerAndRevalidatesReady(t *testing.T) {
	now := time.Now().UTC()
	base, err := domain.NewSourceArtifact(domain.SourceArtifactInput{ID: "artifact", TenantID: "tenant-a", UserID: "user-a", SHA256: apiArtifactDigest, SizeBytes: 100}, now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{Subject: "user-b", TenantID: "tenant-b", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	store := &fakeArtifactStore{head: objectstore.ObjectInfo{SizeBytes: 100, Metadata: map[string]string{"sha256": apiArtifactDigest}}}
	response := performArtifactRequest(artifactTestRouter(t, &fakeSourceArtifactRepository{artifact: &base}, store, principal, now), http.MethodPost, "/api/v1/source-artifacts/artifact/complete", `{}`)
	if response.Code != http.StatusNotFound || store.headCalls != 0 {
		t.Fatalf("cross-owner result status=%d head=%d", response.Code, store.headCalls)
	}
	ready, err := base.MarkReady(now)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeSourceArtifactRepository{artifact: &ready}
	principal = auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response = performArtifactRequest(artifactTestRouter(t, repo, store, principal, now.Add(time.Minute)), http.MethodPost, "/api/v1/source-artifacts/artifact/complete", `{}`)
	if response.Code != http.StatusOK || store.headCalls != 1 || repo.markedReady != 1 {
		t.Fatalf("idempotent complete status=%d head=%d mark=%d body=%s", response.Code, store.headCalls, repo.markedReady, response.Body.String())
	}
}

func TestSourceArtifactHandlersReturnGenericRepositoryFailures(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeSourceArtifactRepository{createErr: errors.New("db contains tenants/private/users/private object key")}
	store := &fakeArtifactStore{presign: objectstore.PresignedPut{URL: "https://secret.example?signature=secret"}}
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, "/api/v1/source-artifacts", `{"sha256":"`+apiArtifactDigest+`","sizeBytes":100}`)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("internal details leaked: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSourceArtifactRecoveryQuotaExceededReturns429(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	ready := readyRecoveryFixture(t, now)
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "create", path: "/api/v1/source-artifacts", body: `{"sha256":"` + apiArtifactDigest + `","sizeBytes":100}`},
		{name: "complete", path: "/api/v1/source-artifacts/ready-artifact/complete", body: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := ready
			repo := &fakeSourceArtifactRepository{artifact: &copy, reopenErr: repositories.ErrSourceArtifactQuotaExceeded}
			store := &fakeArtifactStore{headErr: objectstore.ErrNotFound}
			response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, test.path, test.body)
			if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), `"code":"SOURCE_ARTIFACT_QUOTA_EXCEEDED"`) {
				t.Fatalf("quota recovery response: status=%d body=%s", response.Code, response.Body.String())
			}
			if store.presignCalls != 0 {
				t.Fatalf("quota recovery must not presign: calls=%d", store.presignCalls)
			}
		})
	}
}
