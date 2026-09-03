package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type fakeArtifactLister struct {
	taskRoot string
	path     string
	page     objectstore.ArtifactPage
}

type fakeArtifactReader struct {
	taskRoot string
	path     string
	content  string
	info     objectstore.ArtifactRead
}

func (reader *fakeArtifactReader) ReadArtifact(_ context.Context, taskRoot, relativePath string) (objectstore.ArtifactRead, error) {
	reader.taskRoot, reader.path = taskRoot, relativePath
	result := reader.info
	result.Content = io.NopCloser(strings.NewReader(reader.content))
	return result, nil
}

func (lister *fakeArtifactLister) ListArtifactEntries(_ context.Context, taskRoot, relativePath, _ string, _ int) (objectstore.ArtifactPage, error) {
	lister.taskRoot, lister.path = taskRoot, relativePath
	return lister.page, nil
}

func artifactJob(id, tenantID string) domain.TrainingJob {
	return domain.TrainingJob{
		ID: id, TenantID: tenantID,
		Spec: domain.JobSpec{ResolvedStorage: domain.ResolvedStorageMounts{Output: &domain.ResolvedStorageMount{
			AssetID: "outputs", ClaimName: "tos-outputs", RelativePath: "runs/" + id,
			MountPath: domain.StorageMountOutput, ReadOnly: false,
		}}},
	}
}

func artifactRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func TestJobArtifactRouteRejectsOtherTenant(t *testing.T) {
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{artifactJob("job-tenant-b", "tenant-b")}}
	handler := NewHandler(repository, Options{})
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-tenant-b/artifacts", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestJobArtifactRouteListsOnlyTaskRelativeEntries(t *testing.T) {
	modified := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{artifactJob("job-a", "tenant-a")}}
	assets := &fakeStorageAssetStore{assets: []domain.StorageAsset{{
		ID: "outputs", Name: "训练产物", Kind: domain.StorageAssetOutput, Provider: domain.StorageProviderTOS,
		ClaimName: "tos-outputs", RootPrefix: "platform/tenant-a/outputs", BrowseEnabled: true,
	}}}
	lister := &fakeArtifactLister{page: objectstore.ArtifactPage{Entries: []objectstore.ArtifactEntry{
		{Name: "checkpoints", Type: objectstore.ArtifactDirectory},
		{Name: "metrics.json", Type: objectstore.ArtifactFile, SizeBytes: 128, LastModified: modified},
	}}}
	handler := NewHandler(repository, Options{StorageAssets: assets, ArtifactLister: lister})
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if lister.taskRoot != "platform/tenant-a/outputs/runs/job-a" || lister.path != "" {
		t.Fatalf("unexpected artifact request root=%q path=%q", lister.taskRoot, lister.path)
	}
	body := response.Body.String()
	if !strings.Contains(body, "checkpoints") || !strings.Contains(body, "metrics.json") || strings.Contains(body, "platform/tenant-a/outputs") || strings.Contains(body, "tos-outputs") {
		t.Fatalf("response leaked storage detail or omitted entries: %s", body)
	}
}

func TestJobArtifactPreviewReturnsOnlySafeTaskRelativeContent(t *testing.T) {
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{artifactJob("job-a", "tenant-a")}}
	assets := &fakeStorageAssetStore{assets: []domain.StorageAsset{{
		ID: "outputs", Name: "训练产物", Kind: domain.StorageAssetOutput, Provider: domain.StorageProviderTOS,
		ClaimName: "tos-outputs", RootPrefix: "platform/tenant-a/outputs", BrowseEnabled: true,
	}}}
	reader := &fakeArtifactReader{content: `{"loss":0.25}`, info: objectstore.ArtifactRead{SizeBytes: 13, ContentType: "application/json"}}
	handler := NewHandler(repository, Options{StorageAssets: assets, ArtifactReader: reader})
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts/preview?path=metrics.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if reader.taskRoot != "platform/tenant-a/outputs/runs/job-a" || reader.path != "metrics.json" {
		t.Fatalf("unsafe preview request root=%q path=%q", reader.taskRoot, reader.path)
	}
	var body struct {
		Data struct {
			Kind    string `json:"kind"`
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if body.Data.Kind != "text" || body.Data.Content != `{"loss":0.25}` || strings.Contains(response.Body.String(), "platform/tenant-a/outputs") {
		t.Fatalf("unexpected preview response: %s", response.Body.String())
	}
}

func downloadHandlerFixture() (*fakeArtifactReader, *Handler) {
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{artifactJob("job-a", "tenant-a")}}
	assets := &fakeStorageAssetStore{assets: []domain.StorageAsset{{
		ID: "outputs", Name: "训练产物", Kind: domain.StorageAssetOutput, Provider: domain.StorageProviderTOS,
		ClaimName: "tos-outputs", RootPrefix: "platform/tenant-a/outputs", BrowseEnabled: true,
	}}}
	reader := &fakeArtifactReader{content: "weights", info: objectstore.ArtifactRead{SizeBytes: 7, ContentType: "application/octet-stream"}}
	return reader, NewHandler(repository, Options{StorageAssets: assets, ArtifactReader: reader})
}

func TestJobArtifactDownloadStreamsCheckpointToOwner(t *testing.T) {
	reader, handler := downloadHandlerFixture()
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts/download?path=run_dir/epoch_20.pth", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("approved download must succeed: status=%d body=%q", response.Code, response.Body.String())
	}
	if response.Body.String() != "weights" {
		t.Fatalf("download must stream the stored object: body=%q", response.Body.String())
	}
	if got := response.Header().Get("Content-Disposition"); got != `attachment; filename="epoch_20.pth"` {
		t.Fatalf("download must be sent as an attachment: %q", got)
	}
	if reader.path != "run_dir/epoch_20.pth" {
		t.Fatalf("download must read the requested task-relative path: %q", reader.path)
	}
}

func TestJobArtifactDownloadRejectsNonCheckpointFile(t *testing.T) {
	reader, handler := downloadHandlerFixture()
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts/download?path=run_dir/train.log", nil))
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("download must be limited to checkpoints: status=%d", response.Code)
	}
	if reader.path != "" {
		t.Fatalf("a rejected content type must not read object storage: %q", reader.path)
	}
}

func TestJobArtifactDownloadRejectsOtherTenant(t *testing.T) {
	reader, handler := downloadHandlerFixture()
	router := artifactRouter(handler, auth.Principal{Subject: "user-b", TenantID: "tenant-b", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts/download?path=run_dir/epoch_20.pth", nil))
	if response.Code == http.StatusOK {
		t.Fatalf("a caller must not reach another tenant's checkpoints")
	}
	if reader.path != "" {
		t.Fatalf("cross-tenant download must not read object storage: %q", reader.path)
	}
}

func TestJobArtifactRouteBrowsesOnlyOwnersLogicalDataSpaceOutput(t *testing.T) {
	job := artifactJob("job-a", "tenant-a")
	job.UserID = "user-a"
	job.Spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{Output: &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", SubPath: "runs/job-a", MountPath: domain.DataMountOutputPath,
		ReadOnly: false,
	}}
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{job}}
	lister := &fakeArtifactLister{page: objectstore.ArtifactPage{Entries: []objectstore.ArtifactEntry{{Name: "final", Type: objectstore.ArtifactDirectory}}}}
	handler := NewHandler(repository, Options{ArtifactLister: lister})
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if lister.taskRoot != "ray-train/tenants/tenant-a/users/user-a/runs/job-a" || strings.Contains(response.Body.String(), "ray-train/") {
		t.Fatalf("unexpected logical output root=%q body=%s", lister.taskRoot, response.Body.String())
	}

	otherRouter := artifactRouter(handler, auth.Principal{Subject: "user-b", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	otherResponse := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts", nil))
	if otherResponse.Code != http.StatusForbidden || lister.taskRoot != "ray-train/tenants/tenant-a/users/user-a/runs/job-a" {
		t.Fatalf("logical personal output must not be browsed by another user: status=%d root=%q body=%s", otherResponse.Code, lister.taskRoot, otherResponse.Body.String())
	}
}

func TestJobArtifactRouteBrowsesTenantRootPVCOutput(t *testing.T) {
	job := artifactJob("job-a", "tenant-a")
	job.UserID = "user-a"
	job.Spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{Output: &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/user-a/runs/experiments/job-a", MountPath: domain.DataMountOutputPath,
		ReadOnly: false,
	}}
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{job}}
	lister := &fakeArtifactLister{page: objectstore.ArtifactPage{Entries: []objectstore.ArtifactEntry{{Name: "e2e-train-result.json", Type: objectstore.ArtifactFile}}}}
	handler := NewHandler(repository, Options{ArtifactLister: lister})
	router := artifactRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/artifacts", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if lister.taskRoot != "ray-train/tenants/tenant-a/users/user-a/runs/experiments/job-a" || strings.Contains(response.Body.String(), "ray-train/") {
		t.Fatalf("unexpected tenant-root artifact request root=%q body=%s", lister.taskRoot, response.Body.String())
	}
}
