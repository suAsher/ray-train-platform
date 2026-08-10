package rayapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

type recoveryStore struct {
	objects map[string]objectstore.ObjectInfo
	headErr error
	putBody []byte
}

func (store *recoveryStore) PresignPut(context.Context, string, string, int64, time.Duration) (objectstore.PresignedPut, error) {
	return objectstore.PresignedPut{}, errors.New("not used")
}

func (store *recoveryStore) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	if store.headErr != nil {
		return objectstore.ObjectInfo{}, store.headErr
	}
	info, ok := store.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return info, nil
}

func (store *recoveryStore) Put(_ context.Context, key, digest string, sizeBytes int64, body io.Reader) error {
	payload, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	store.putBody = append([]byte(nil), payload...)
	if store.objects == nil {
		store.objects = make(map[string]objectstore.ObjectInfo)
	}
	store.objects[key] = objectstore.ObjectInfo{SizeBytes: sizeBytes, Metadata: map[string]string{"sha256": digest}}
	return nil
}

type recoveryLogs struct {
	lines        []observability.LogLine
	err          error
	queriedJobID string
}

func (logs *recoveryLogs) QueryJobLogs(_ context.Context, jobID string, _ int) ([]observability.LogLine, error) {
	logs.queriedJobID = jobID
	return append([]observability.LogLine(nil), logs.lines...), logs.err
}

func (repository *rayTestRepository) ReopenSourceArtifactUploadWithLimits(_ context.Context, tenantID, userID, artifactID string, expiresAt time.Time, limits repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	artifact, err := repository.GetSourceArtifact(context.Background(), tenantID, userID, artifactID)
	if err != nil {
		return nil, err
	}
	reopened := *artifact
	reopened.State = domain.SourceArtifactPending
	reopened.CompletedAt = nil
	reopened.UploadExpiresAt = expiresAt.UTC()
	repository.artifacts[artifactID] = reopened
	repository.reopens++
	repository.limits = limits
	return &reopened, nil
}

func recoveryRouter(t *testing.T, repository *rayTestRepository, store objectstore.Store, logs api.LogProvider, principal auth.Principal) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{NewID: func() (string, error) { return "job-ray", nil }})
	handler, err := NewHandler(repository, store, submission, Options{SpoolDir: t.TempDir(), Logs: logs, TailPollInterval: time.Millisecond, Now: func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("new Ray API handler: %v", err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterRoutes(router.Group("/ray"))
	return router
}

func recoveryRequest(router http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestRayPackageReadyObjectLifecycleChecksCanonicalStore(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &recoveryStore{}
	router := recoveryRouter(t, repository, store, &recoveryLogs{}, principal)
	packageName := testPackageSHA256 + ".zip"
	path := "/ray/api/packages/gcs/" + packageName
	payload := []byte("PK\x03\x04recovery")

	if response := recoveryRequest(router, http.MethodPut, path, payload); response.Code != http.StatusOK {
		t.Fatalf("initial put status=%d body=%s", response.Code, response.Body.String())
	}
	if response := recoveryRequest(router, http.MethodGet, path, nil); response.Code != http.StatusOK {
		t.Fatalf("healthy ready object status=%d body=%s", response.Code, response.Body.String())
	}
	artifactID := rayPackageArtifactID(principal.TenantID, principal.Subject, packageName)
	artifact, err := repository.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	delete(store.objects, artifact.ObjectKey)
	if response := recoveryRequest(router, http.MethodGet, path, nil); response.Code != http.StatusNotFound {
		t.Fatalf("missing ready object GET status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.reopens != 1 || repository.limits != repositories.DefaultSourceArtifactLimits() {
		t.Fatalf("missing object did not owner-safely reopen with configured limits: reopens=%d limits=%+v", repository.reopens, repository.limits)
	}
	if response := recoveryRequest(router, http.MethodPut, path, payload); response.Code != http.StatusOK {
		t.Fatalf("retransmit after reopen status=%d body=%s", response.Code, response.Body.String())
	}
	artifact, err = repository.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, artifactID)
	if err != nil || artifact.State != domain.SourceArtifactReady {
		t.Fatalf("retransmit did not restore ready artifact: artifact=%+v err=%v", artifact, err)
	}

	store.headErr = objectstore.ErrUnavailable
	response := recoveryRequest(router, http.MethodGet, path, nil)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" {
		t.Fatalf("unavailable store status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
	}
	store.headErr = nil
	store.objects[artifact.ObjectKey] = objectstore.ObjectInfo{SizeBytes: artifact.SizeBytes, Metadata: map[string]string{"sha256": "mismatch"}}
	if response := recoveryRequest(router, http.MethodGet, path, nil); response.Code != http.StatusConflict {
		t.Fatalf("mismatched ready object GET status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRayLogsUseOwnerScopedLogProviderForSDKPaths(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &recoveryStore{}
	logs := &recoveryLogs{lines: []observability.LogLine{{Line: "ray-sdk-log-marker"}}}
	router := recoveryRouter(t, repository, store, logs, principal)
	packageName := testPackageSHA256 + ".zip"
	if response := recoveryRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, []byte("PK\x03\x04logs")); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	if response := recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName))); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	for _, suffix := range []string{"/logs"} {
		response := recoveryRequest(router, http.MethodGet, "/ray/api/jobs/raysubmit_test"+suffix, nil)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("ray-sdk-log-marker")) {
			t.Fatalf("SDK logs %s status=%d body=%s", suffix, response.Code, response.Body.String())
		}
	}
	if logs.queriedJobID != "job-ray" {
		t.Fatalf("logs queried wrong or unscoped job ID: %q", logs.queriedJobID)
	}
}

func TestRayHandlerRequiresExplicitSpoolDirectory(t *testing.T) {
	repository := &rayTestRepository{}
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{})
	if _, err := NewHandler(repository, &recoveryStore{}, submission, Options{}); err == nil {
		t.Fatal("missing spool directory was accepted")
	}
}

func TestRay235TransportPackageSubmitsThroughSubmissionService(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	router := recoveryRouter(t, repository, &recoveryStore{}, &recoveryLogs{}, principal)
	packageName := "_ray_pkg_" + testRayPackageSHA1 + ".zip"
	if response := recoveryRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, []byte("PK\x03\x04ray235")); response.Code != http.StatusOK {
		t.Fatalf("Ray 2.35 package upload status=%d body=%s", response.Code, response.Body.String())
	}
	if response := recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName))); response.Code != http.StatusOK {
		t.Fatalf("Ray 2.35 package submit status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.created == nil || repository.created.SourceArtifactID != rayPackageArtifactID(principal.TenantID, principal.Subject, packageName) {
		t.Fatalf("Ray 2.35 package was not owner-scoped into SubmissionService: %+v", repository.created)
	}
}

func TestRaySubmitRequiresHealthyReadyArtifactObject(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &recoveryStore{}
	router := recoveryRouter(t, repository, store, &recoveryLogs{}, principal)
	packageName := testPackageSHA256 + ".zip"
	packagePath := "/ray/api/packages/gcs/" + packageName
	payload := []byte("PK\x03\x04submit-recovery")
	if response := recoveryRequest(router, http.MethodPut, packagePath, payload); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	artifactID := rayPackageArtifactID(principal.TenantID, principal.Subject, packageName)
	artifact, err := repository.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	delete(store.objects, artifact.ObjectKey)
	response := recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName)))
	if response.Code != http.StatusNotFound || repository.reopens != 1 || repository.created != nil {
		t.Fatalf("missing artifact submit status=%d reopens=%d created=%+v", response.Code, repository.reopens, repository.created)
	}
	if response = recoveryRequest(router, http.MethodPut, packagePath, payload); response.Code != http.StatusOK {
		t.Fatalf("reupload status=%d body=%s", response.Code, response.Body.String())
	}
	artifact, err = repository.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, artifactID)
	if err != nil || artifact.State != domain.SourceArtifactReady {
		t.Fatalf("reupload did not restore ready state: artifact=%+v err=%v", artifact, err)
	}
	store.headErr = objectstore.ErrUnavailable
	response = recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName)))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" || repository.created != nil {
		t.Fatalf("unavailable artifact submit status=%d retry=%q created=%+v", response.Code, response.Header().Get("Retry-After"), repository.created)
	}
	store.headErr = nil
	store.objects[artifact.ObjectKey] = objectstore.ObjectInfo{SizeBytes: artifact.SizeBytes, Metadata: map[string]string{"sha256": "mismatch"}}
	response = recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName)))
	if response.Code != http.StatusConflict || repository.created != nil {
		t.Fatalf("mismatched artifact submit status=%d created=%+v", response.Code, repository.created)
	}
}
