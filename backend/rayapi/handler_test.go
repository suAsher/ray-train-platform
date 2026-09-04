package rayapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
	"ray-train-platform-backend/runtimecatalog"
)

type rayTestRepository struct {
	jobs      []domain.TrainingJob
	artifacts map[string]domain.SourceArtifact
	bindings  []domain.DataMountBinding
	created   *domain.TrainingJob
	identity  int
	canceled  string
	reopens   int
	limits    repositories.SourceArtifactLimits
}

type managedRayImageStore struct{}

func (managedRayImageStore) CreateImage(context.Context, domain.PlatformImage) error { return nil }
func (managedRayImageStore) ListImages(_ context.Context, _ string, kind string) ([]domain.PlatformImage, error) {
	if kind != domain.ImageKindTraining {
		return nil, nil
	}
	return []domain.PlatformImage{{
		ID: "managed-native", Name: "managed-native", Kind: domain.ImageKindTraining, Reference: testImageDigest,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}}, nil
}
func (managedRayImageStore) DefaultImage(context.Context, string, string) (domain.PlatformImage, error) {
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}
func (managedRayImageStore) ImageByReference(context.Context, string, string, string) (domain.PlatformImage, error) {
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}
func (managedRayImageStore) SetImageShared(context.Context, string, string, bool, string) (domain.PlatformImage, error) {
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}
func (managedRayImageStore) DeleteImage(context.Context, string, string, bool) error { return nil }

type streamingRayImageStore struct{ managedRayImageStore }

func (streamingRayImageStore) ListImages(_ context.Context, _ string, kind string) ([]domain.PlatformImage, error) {
	if kind != domain.ImageKindTraining {
		return nil, nil
	}
	return []domain.PlatformImage{{
		ID: "streaming-native", Name: "streaming-native", Kind: domain.ImageKindTraining, Reference: testImageDigest,
		RayVersion: domain.RayVersionCanary, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}}, nil
}

type rayDatasetCatalog struct {
	dataset domain.Dataset
	version domain.DatasetVersion
}

func (catalog rayDatasetCatalog) CreateDataset(context.Context, domain.Dataset) error { return nil }
func (catalog rayDatasetCatalog) GetDataset(_ context.Context, _ string, _ bool, datasetID string) (domain.Dataset, error) {
	if datasetID != catalog.dataset.ID {
		return domain.Dataset{}, repositories.ErrDatasetNotFound
	}
	return catalog.dataset, nil
}
func (catalog rayDatasetCatalog) ListDatasets(context.Context, string, bool) ([]domain.Dataset, error) {
	return []domain.Dataset{catalog.dataset}, nil
}
func (catalog rayDatasetCatalog) GetDatasetVersion(_ context.Context, _ string, _ bool, datasetID, versionID string) (domain.DatasetVersion, error) {
	if datasetID != catalog.dataset.ID || versionID != catalog.version.ID {
		return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotFound
	}
	return catalog.version, nil
}
func (catalog rayDatasetCatalog) ListDatasetVersions(_ context.Context, _ string, _ bool, datasetID string) ([]domain.DatasetVersion, error) {
	if datasetID != catalog.dataset.ID {
		return nil, repositories.ErrDatasetNotFound
	}
	return []domain.DatasetVersion{catalog.version}, nil
}
func (catalog rayDatasetCatalog) GetDatasetPublicationRunForVersion(context.Context, string, bool, string, string) (domain.DatasetPublicationRun, error) {
	return domain.DatasetPublicationRun{}, repositories.ErrDatasetPublicationRunNotFound
}
func (catalog rayDatasetCatalog) ResolveReadyDatasetVersion(_ context.Context, _ string, _ bool, datasetID string, selector domain.DatasetVersionSelector) (domain.DatasetVersion, error) {
	if datasetID != catalog.dataset.ID || catalog.version.State != domain.DatasetVersionReady || (!selector.Latest && selector.VersionID != catalog.version.ID) {
		return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotReady
	}
	return catalog.version, nil
}
func (catalog rayDatasetCatalog) TransitionDatasetVersion(context.Context, string, string, domain.DatasetVersionState) (domain.DatasetVersion, error) {
	return domain.DatasetVersion{}, errors.New("not implemented")
}

func readyRayPersonalDataBinding(tenantID, userID string) domain.DataMountBinding {
	return domain.DataMountBinding{
		ID: "personal-" + userID, TenantID: tenantID, UserID: userID,
		Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-" + userID, Status: domain.DataMountBindingReady,
	}
}

func (repository *rayTestRepository) ListDataBindings(_ context.Context, tenantID, userID string) ([]domain.DataMountBinding, error) {
	if repository.bindings != nil {
		return repository.bindings, nil
	}
	return []domain.DataMountBinding{readyRayPersonalDataBinding(tenantID, userID)}, nil
}

func (repository *rayTestRepository) EnsurePersonalDataBinding(_ context.Context, binding domain.DataMountBinding) (domain.DataMountBinding, error) {
	return binding, nil
}

func (repository *rayTestRepository) Create(_ context.Context, job *domain.TrainingJob, _ string) error {
	copy := *job
	repository.jobs = append(repository.jobs, copy)
	repository.created = &copy
	return nil
}

func (repository *rayTestRepository) Get(_ context.Context, tenantID, id string) (*domain.TrainingJob, error) {
	for _, job := range repository.jobs {
		if job.TenantID == tenantID && (job.ID == id || job.ExternalSubmissionID == id) {
			copy := job
			return &copy, nil
		}
	}
	return nil, repositories.ErrSourceArtifactNotFound
}

func (repository *rayTestRepository) List(_ context.Context, filter domain.JobFilter) (domain.Page[domain.TrainingJob], error) {
	items := make([]domain.TrainingJob, 0, len(repository.jobs))
	for _, job := range repository.jobs {
		if job.TenantID == filter.TenantID {
			items = append(items, job)
		}
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	start := filter.Offset
	if start < 0 {
		start = 0
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return domain.Page[domain.TrainingJob]{Items: items[start:end], Limit: limit, Offset: start, Total: int64(len(items))}, nil
}

func (repository *rayTestRepository) SetDesiredState(_ context.Context, tenantID, id string, state domain.DesiredState) error {
	for index, job := range repository.jobs {
		if job.TenantID == tenantID && (job.ID == id || job.ExternalSubmissionID == id) {
			repository.jobs[index].DesiredState = state
			repository.canceled = tenantID + "/" + id
			return nil
		}
	}
	return repositories.ErrSourceArtifactNotFound
}

func (repository *rayTestRepository) EnsureIdentity(context.Context, auth.Principal) error {
	repository.identity++
	return nil
}

func (repository *rayTestRepository) CreateOrReuseSourceArtifactWithLimits(_ context.Context, artifact *domain.SourceArtifact, _ repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	if repository.artifacts == nil {
		repository.artifacts = make(map[string]domain.SourceArtifact)
	}
	for _, existing := range repository.artifacts {
		if existing.TenantID == artifact.TenantID && existing.UserID == artifact.UserID && existing.SHA256 == artifact.SHA256 {
			copy := existing
			return &copy, nil
		}
	}
	copy := *artifact
	repository.artifacts[artifact.ID] = copy
	return &copy, nil
}

func (repository *rayTestRepository) GetSourceArtifact(_ context.Context, tenantID, userID, artifactID string) (*domain.SourceArtifact, error) {
	artifact, ok := repository.artifacts[artifactID]
	if !ok || artifact.TenantID != tenantID || artifact.UserID != userID {
		return nil, repositories.ErrSourceArtifactNotFound
	}
	copy := artifact
	return &copy, nil
}

func (repository *rayTestRepository) MarkSourceArtifactReady(_ context.Context, tenantID, userID, artifactID string, completedAt time.Time) (*domain.SourceArtifact, error) {
	artifact, err := repository.GetSourceArtifact(context.Background(), tenantID, userID, artifactID)
	if err != nil {
		return nil, err
	}
	ready, err := artifact.MarkReady(completedAt)
	if err != nil {
		return nil, err
	}
	repository.artifacts[artifactID] = ready
	return &ready, nil
}

type rayTestStore struct {
	objects map[string]objectstore.ObjectInfo
	putErr  error
	putBody []byte
}

func (store *rayTestStore) PresignPut(context.Context, string, string, int64, time.Duration) (objectstore.PresignedPut, error) {
	return objectstore.PresignedPut{}, errors.New("not used")
}

func (store *rayTestStore) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	info, ok := store.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return info, nil
}

func (store *rayTestStore) Put(_ context.Context, key, digest string, sizeBytes int64, body io.Reader) error {
	if store.putErr != nil {
		return store.putErr
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	store.putBody = append([]byte(nil), data...)
	if store.objects == nil {
		store.objects = make(map[string]objectstore.ObjectInfo)
	}
	store.objects[key] = objectstore.ObjectInfo{SizeBytes: sizeBytes, Metadata: map[string]string{"sha256": digest}}
	return nil
}

func rayRouter(t *testing.T, repository *rayTestRepository, store *rayTestStore, principal auth.Principal) *gin.Engine {
	return rayRouterWithCachePolicy(t, repository, store, principal, api.LocalCachePolicy{})
}

func rayRouterForRepository(t *testing.T, repository Repository, store objectstore.Store, principal auth.Principal) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{
		DataSpaces: repository, DataSpacesEnabled: true,
		NewID: func() (string, error) { return "job-ray", nil },
	})
	handler, err := NewHandler(repository, store, submission, Options{
		SpoolDir: t.TempDir(), Defaults: SubmissionDefaults{
			Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi",
		}, Logs: &recoveryLogs{lines: []observability.LogLine{{Line: "ray-sdk-log-marker"}}},
		Now: func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) },
	})
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

func rayRouterWithCachePolicy(t *testing.T, repository *rayTestRepository, store *rayTestStore, principal auth.Principal, cache api.LocalCachePolicy) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{
		DataSpaces: repository, DataSpacesEnabled: true,
		NewID:      func() (string, error) { return "job-ray", nil },
		LocalCache: cache,
	})
	handler, err := NewHandler(repository, store, submission, Options{SpoolDir: t.TempDir(), Defaults: SubmissionDefaults{Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"}, Logs: &recoveryLogs{lines: []observability.LogLine{{Line: "ray-sdk-log-marker"}}}, Now: func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) }})
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

func rayRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func raySubmitBody(packageName string) string {
	return `{"entrypoint":"python train.py","submission_id":"raysubmit_test","runtime_env":{"working_dir":"gcs://` + packageName + `"},"metadata":{"ray-platform.image":"` + testImageDigest + `","ray-platform.worker-replicas":"1","ray-platform.gpus-per-worker":"1","ray-platform.cpu-per-worker":"8","ray-platform.memory-per-worker":"32Gi","ray-platform.queue":"tenant-a-gpu"}}`
}

func TestRayRoutesReturnRayJobsProtocolAndRuntimeVersion(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response := rayRequest(rayRouter(t, &rayTestRepository{}, &rayTestStore{}, principal), http.MethodGet, "/ray/api/version", "")
	if response.Code != http.StatusOK {
		t.Fatalf("version status=%d body=%s", response.Code, response.Body.String())
	}
	var version map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version["version"] != "4" || version["ray_version"] != "2.35.0" {
		t.Fatalf("unexpected version response: %v", version)
	}
	if _, ok := version["ray_commit"]; !ok {
		t.Fatalf("version response is missing ray_commit: %v", version)
	}
	if _, ok := version["session_name"]; !ok {
		t.Fatalf("version response is missing session_name: %v", version)
	}
}

func TestRayVersionResponseUsesConfiguredRuntimeAndLegacyFallback(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	for _, test := range []struct {
		name       string
		configured string
		want       string
	}{
		{name: "configured", configured: domain.RayVersionProduction, want: domain.RayVersionProduction},
		{name: "blank falls back", configured: "  ", want: domain.RayVersionLegacy},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &rayTestRepository{}
			submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{DataSpaces: repository, DataSpacesEnabled: true})
			handler, err := NewHandler(repository, &rayTestStore{}, submission, Options{SpoolDir: t.TempDir(), RayVersion: test.configured})
			if err != nil {
				t.Fatalf("new handler: %v", err)
			}
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal) })
			handler.RegisterRoutes(router.Group("/ray"))
			response := rayRequest(router, http.MethodGet, "/ray/api/version", "")
			var version map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
				t.Fatal(err)
			}
			if version["ray_version"] != test.want {
				t.Fatalf("ray_version=%q want %q", version["ray_version"], test.want)
			}
		})
	}
}

func TestPersonalSourceArtifactRootUsesPersistedStorageKeyBinding(t *testing.T) {
	repository := &rayTestRepository{bindings: []domain.DataMountBinding{{
		ID: "personal-oidc-user", TenantID: "local", UserID: "oidc-subject-123",
		Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		RootPrefix: "ray-train/tenants/local/users/guofeng.su/", Status: domain.DataMountBindingReady,
	}}}
	handler := &Handler{repository: repository}
	root, err := handler.personalSourceArtifactRoot(context.Background(), auth.Principal{
		Subject: "oidc-subject-123", Username: "guofeng.su", TenantID: "local",
	})
	if err != nil {
		t.Fatalf("resolve persisted personal root: %v", err)
	}
	if root != "ray-train/tenants/local/users/guofeng.su/" {
		t.Fatalf("root=%q", root)
	}
}

func TestPersonalSourceArtifactRootUsesUsernameForFirstUpload(t *testing.T) {
	handler := &Handler{repository: &rayTestRepository{bindings: []domain.DataMountBinding{}}}
	root, err := handler.personalSourceArtifactRoot(context.Background(), auth.Principal{
		Subject: "oidc-subject-123", Username: "Guofeng.Su", TenantID: "local",
	})
	if err != nil {
		t.Fatalf("resolve first-upload personal root: %v", err)
	}
	if root != "ray-train/tenants/local/users/guofeng.su/" {
		t.Fatalf("root=%q", root)
	}
}

func TestRayPackagePutHeadAndSubmitAreOwnerScoped(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &rayTestStore{}
	router := rayRouter(t, repository, store, principal)
	packageName := testPackageSHA256 + ".zip"
	payload := []byte("PK\x03\x04payload")
	request := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+packageName, bytes.NewReader(payload))
	request.Header.Set("Content-Length", "11")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("package put status=%d body=%s", response.Code, response.Body.String())
	}
	artifactID := rayPackageArtifactID(principal.TenantID, principal.Subject, packageName)
	if got := response.Header().Get(httpapi.SourceArtifactIDHeader); got != artifactID {
		t.Fatalf("package put artifact header=%q want %q", got, artifactID)
	}
	if !bytes.Equal(store.putBody, payload) {
		t.Fatalf("store body=%q, want %q", store.putBody, payload)
	}
	computed := sha256.Sum256(payload)
	artifact, err := repository.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, artifactID)
	if err != nil || artifact.State != domain.SourceArtifactReady || artifact.SHA256 != hex.EncodeToString(computed[:]) {
		t.Fatalf("package was not persisted as a ready canonical artifact: artifact=%+v err=%v", artifact, err)
	}
	if response = rayRequest(router, http.MethodHead, "/ray/api/packages/gcs/"+packageName, ""); response.Code != http.StatusOK {
		t.Fatalf("package head status=%d", response.Code)
	}
	if got := response.Header().Get(httpapi.SourceArtifactIDHeader); got != artifactID {
		t.Fatalf("package head artifact header=%q want %q", got, artifactID)
	}
	retry := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+packageName, bytes.NewReader(payload))
	retry.ContentLength = int64(len(payload))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, retry)
	if response.Code != http.StatusOK || response.Header().Get(httpapi.SourceArtifactIDHeader) != artifactID {
		t.Fatalf("idempotent package put status=%d artifact=%q", response.Code, response.Header().Get(httpapi.SourceArtifactIDHeader))
	}
	if response = rayRequest(router, http.MethodPost, "/ray/api/jobs/", raySubmitBody(packageName)); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.created == nil || repository.created.SubmissionOrigin != domain.SubmissionOriginRayCLI || repository.created.SourceArtifactID != artifactID || repository.created.Spec.Source.Type != "workspace-archive" || repository.created.Spec.Source.ArtifactSHA256 != artifact.SHA256 {
		t.Fatalf("Ray submit did not use the owner-scoped workspace archive flow: %+v", repository.created)
	}
	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/ray/api/jobs/"},
		{method: http.MethodGet, path: "/ray/api/jobs/raysubmit_test"},
		{method: http.MethodGet, path: "/ray/api/jobs/raysubmit_test/logs"},
		{method: http.MethodPost, path: "/ray/api/jobs/raysubmit_test/stop"},
		{method: http.MethodDelete, path: "/ray/api/jobs/raysubmit_test"},
	} {
		if response = rayRequest(router, endpoint.method, endpoint.path, ""); response.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", endpoint.method, endpoint.path, response.Code, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), `"deleted":true`) {
		t.Fatalf("delete response is not Ray-compatible: %s", response.Body.String())
	}
}

func TestManagedNativeSubmitPersistsOwnerScopedServerOutput(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &rayTestStore{}
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{
		Images: managedRayImageStore{}, RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
		DataSpaces: repository, DataSpacesEnabled: true, NewID: func() (string, error) { return "job-native-managed", nil },
	})
	handler, err := NewHandler(repository, store, submission, Options{
		SpoolDir: t.TempDir(), Defaults: SubmissionDefaults{Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"},
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal) })
	handler.RegisterRoutes(router.Group("/ray"))
	packageName := testPackageSHA256 + ".zip"
	if response := rayRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, "PKpayload"); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	body := `{"entrypoint":"python train.py","submission_id":"managed_native","runtime_env":{"working_dir":"gcs://` + packageName + `"},"metadata":{"platform.training.engine":"ray-train"}}`
	if response := rayRequest(router, http.MethodPost, "/ray/api/jobs/", body); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	job := repository.created
	if job == nil || job.Spec.Output != (domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "native-ray"}) {
		t.Fatalf("managed native output was not server-selected: %+v", job)
	}
	output := job.Spec.ResolvedDataMounts.Output
	if output == nil || output.Space != domain.DataSpaceMyRuns || output.BindingSpace != domain.DataSpaceWorkspace || output.ClaimName != "data-user-a" || output.SubPath != "runs/native-ray/job-native-managed" || output.MountPath != domain.DataMountOutputPath || output.ReadOnly {
		t.Fatalf("managed native output was not owner-scoped: %+v", output)
	}
}

func TestNativeRaySubmitPinsResolvedStreamingDatasetProvenance(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &rayTestStore{}
	dataset := domain.Dataset{
		ID: "dataset-labeled-full", Slug: "labeled-full", Name: "Labeled full", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "s1h-v1",
	}
	version := domain.DatasetVersion{
		ID: "version-20260830", DatasetID: dataset.ID, Version: "2026.08.30", State: domain.DatasetVersionReady,
		ManifestSHA256:    strings.Repeat("a", 64),
		ManifestObjectKey: domain.DefaultDatasetInternalPrefix + "/" + dataset.ID + "/manifests/version-20260830.parquet",
		SchemaVersion:     dataset.SchemaVersion, SourceObjectCount: 100, TrainSamples: 80, ValSamples: 20,
	}
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{
		Images: streamingRayImageStore{}, RuntimePolicy: runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}),
		DataSpaces: repository, DataSpacesEnabled: true, Datasets: rayDatasetCatalog{dataset: dataset, version: version},
		DatasetVersioningEnabled: true, RayDataStreamingEnabled: true, DatasetInternalPrefix: domain.DefaultDatasetInternalPrefix,
		NewID: func() (string, error) { return "job-native-streaming", nil },
	})
	handler, err := NewHandler(repository, store, submission, Options{
		SpoolDir: t.TempDir(), Defaults: SubmissionDefaults{
			Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi",
		},
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal) })
	handler.RegisterRoutes(router.Group("/ray"))
	packageName := testPackageSHA256 + ".zip"
	if response := rayRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, "PKpayload"); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	body := `{"entrypoint":"python train.py","submission_id":"streaming_native","runtime_env":{"working_dir":"gcs://` + packageName + `"},"metadata":{"platform.training.engine":"ray-train","platform.dataset.ref":"labeled-full","platform.dataset.version":"latest"}}`
	if response := rayRequest(router, http.MethodPost, "/ray/api/jobs/", body); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	job := repository.created
	if job == nil {
		t.Fatal("native submission did not persist a job")
	}
	if job.Spec.DatasetRef != (domain.DatasetReference{Dataset: dataset.ID, Version: version.ID}) || job.Spec.CachePolicy != domain.DatasetCachePolicyAuto || job.Spec.DataMode != domain.DataModeStreaming {
		t.Fatalf("native dataset reference was not resolved and normalized: %+v", job.Spec)
	}
	wantProvenance := domain.DatasetProvenance{
		DatasetID: dataset.ID, DatasetVersionID: version.ID, ManifestSHA256: version.ManifestSHA256,
		DataMode: domain.DataModeStreaming, CachePolicy: domain.DatasetCachePolicyAuto,
	}
	if job.DatasetProvenance != wantProvenance || job.Spec.RayVersion != domain.RayVersionCanary || job.Spec.TrainingEngine != domain.TrainingEngineRayTrain {
		t.Fatalf("native streaming provenance/runtime was not persisted: job=%+v", job)
	}
}

func TestRayPackagePutIgnoresEarlierRequestScopedArtifactWithSameDigest(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Username: "user-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	payload := []byte("PK\x03\x04request-first")
	digestValue := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestValue[:])
	for _, requestState := range []domain.SourceArtifactState{domain.SourceArtifactReady, domain.SourceArtifactPending} {
		t.Run(string(requestState), func(t *testing.T) {
			database, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.AutoMigrate(&repositories.TenantRecord{}, &repositories.UserRecord{}, &repositories.SourceArtifactRecord{}, &repositories.SourceArtifactRequestRecord{}, &repositories.DataMountBindingRecord{}); err != nil {
				t.Fatal(err)
			}
			repository := repositories.NewGormRepository(database)
			if err := repository.EnsureIdentity(context.Background(), principal); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
			requested, err := domain.NewRequestScopedSourceArtifact(domain.SourceArtifactInput{
				ID: "artifact-0123456789abcdef01234567", TenantID: principal.TenantID, UserID: principal.Subject,
				SHA256: digest, SizeBytes: int64(len(payload)),
			}, now.Add(15*time.Minute), now)
			if err != nil {
				t.Fatal(err)
			}
			storedRequest, err := repository.CreateSourceArtifactForRequestWithLimits(context.Background(), &requested, "source-request-0123456789abcdef01234567", repositories.DefaultSourceArtifactLimits())
			if err != nil {
				t.Fatal(err)
			}
			if requestState == domain.SourceArtifactReady {
				if _, err := repository.MarkSourceArtifactReady(context.Background(), principal.TenantID, principal.Subject, storedRequest.ID, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			store := &rayTestStore{}
			router := rayRouterForRepository(t, repository, store, principal)
			packageName := testPackageSHA256 + ".zip"
			request := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+packageName, bytes.NewReader(payload))
			request.Header.Set("Content-Length", strconv.Itoa(len(payload)))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("Ray package put after %s request status=%d body=%s", requestState, response.Code, response.Body.String())
			}
			legacyID := rayPackageArtifactID(principal.TenantID, principal.Subject, packageName)
			legacy, err := repository.GetSourceArtifact(context.Background(), principal.TenantID, principal.Subject, legacyID)
			if err != nil || legacy.ID == storedRequest.ID || legacy.ObjectKey == storedRequest.ObjectKey {
				t.Fatalf("Ray package did not create its legacy artifact: legacy=%+v request=%+v err=%v", legacy, storedRequest, err)
			}
			wantKey, err := domain.SourceArtifactObjectKey(principal.TenantID, principal.Subject, digest)
			if err != nil || legacy.ObjectKey != wantKey || legacy.State != domain.SourceArtifactReady {
				t.Fatalf("Ray package legacy key/state mismatch: artifact=%+v wantKey=%q err=%v", legacy, wantKey, err)
			}
		})
	}
}

func TestRayStopAndDeleteRequireJobOwnershipOrAdministratorRole(t *testing.T) {
	jobs := []domain.TrainingJob{{
		ID: "job-other", ExternalSubmissionID: "external-other", TenantID: "tenant-a", UserID: "user-b",
		Spec: domain.JobSpec{Name: "other-job"},
	}}

	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/ray/api/jobs/external-other/stop"},
		{method: http.MethodDelete, path: "/ray/api/jobs/external-other"},
	} {
		repository := &rayTestRepository{jobs: append([]domain.TrainingJob(nil), jobs...)}
		engineer := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
		response := rayRequest(rayRouter(t, repository, &rayTestStore{}, engineer), endpoint.method, endpoint.path, "")
		if response.Code != http.StatusNotFound || repository.canceled != "" {
			t.Fatalf("engineer must not mutate another user's Ray job via %s: status=%d canceled=%q body=%s", endpoint.method, response.Code, repository.canceled, response.Body.String())
		}
	}

	repository := &rayTestRepository{jobs: append([]domain.TrainingJob(nil), jobs...)}
	tenantAdmin := auth.Principal{Subject: "team-admin", TenantID: "tenant-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal}
	response := rayRequest(rayRouter(t, repository, &rayTestStore{}, tenantAdmin), http.MethodPost, "/ray/api/jobs/external-other/stop", "")
	if response.Code != http.StatusOK || repository.canceled != "tenant-a/job-other" {
		t.Fatalf("tenant administrator must stop own-tenant jobs: status=%d canceled=%q body=%s", response.Code, repository.canceled, response.Body.String())
	}
}

func TestRayPackageSubmitWithoutPlatformMetadataUsesConfiguredDefaults(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &rayTestStore{}
	router := rayRouter(t, repository, store, principal)
	packageName := testPackageSHA256 + ".zip"
	payload := []byte("PK\\x03\\x04bare")
	request := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+packageName, bytes.NewReader(payload))
	request.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("package upload status=%d body=%s", response.Code, response.Body.String())
	}
	body := `{"entrypoint":"python train.py","submission_id":"bare_cli","runtime_env":{"working_dir":"gcs://` + packageName + `"}}`
	if response = rayRequest(router, http.MethodPost, "/ray/api/jobs/", body); response.Code != http.StatusOK {
		t.Fatalf("bare Ray CLI submit status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.created == nil || repository.created.Spec.Image != testImageDigest || repository.created.Spec.Resources.WorkerReplicas != 1 || repository.created.Spec.Resources.GPUsPerWorker != 1 || repository.created.Spec.Queue != "tenant-a-gpu" {
		t.Fatalf("bare Ray CLI defaults were not normalized by the platform: %+v", repository.created)
	}
}

func TestRayCacheMetadataPersistsThroughSharedSubmissionService(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &rayTestStore{}
	router := rayRouterWithCachePolicy(t, repository, store, principal, api.LocalCachePolicy{
		Enabled: true, AllowedSizes: []string{"100Gi", "200Gi"}, DefaultSize: "200Gi", MaxSize: "500Gi",
	})
	packageName := testPackageSHA256 + ".zip"
	payload := "PK\x03\x04cache"
	request := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+packageName, strings.NewReader(payload))
	request.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("package upload status=%d body=%s", response.Code, response.Body.String())
	}
	body := `{"entrypoint":"python train.py","submission_id":"cache_cli","runtime_env":{"working_dir":"gcs://` + packageName + `"},"metadata":{"platform.cache.mode":"runtime"}}`
	if response = rayRequest(router, http.MethodPost, "/ray/api/jobs/", body); response.Code != http.StatusOK {
		t.Fatalf("cache submit status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.created == nil || repository.created.Spec.Cache.Mode != domain.CacheModeRuntime || repository.created.Spec.Cache.Size != "200Gi" {
		t.Fatalf("cache metadata did not reach persisted normalized JobSpec: %+v", repository.created)
	}
}

func TestRayPackageHandlerRejectsUnsafeNamesAndUnboundedStreams(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	router := rayRouter(t, repository, &rayTestStore{}, principal)
	for _, path := range []string{
		"/ray/api/packages/s3/" + testPackageSHA256 + ".zip",
		"/ray/api/packages/gcs/../x.zip",
		"/ray/api/packages/gcs/working_dir.zip",
	} {
		response := rayRequest(router, http.MethodPut, path, "payload")
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("unsafe package path %q returned %d", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+testPackageSHA256+".zip", strings.NewReader("payload"))
	request.ContentLength = -1
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusLengthRequired || repository.artifacts != nil {
		t.Fatalf("streaming upload status=%d artifacts=%v", response.Code, repository.artifacts)
	}
}

func TestRayPackageAliasConflictDoesNotAssociateDifferentTransportNames(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	router := rayRouter(t, repository, &rayTestStore{}, principal)
	payload := "PK\x03\x04same-content"
	first := testPackageSHA256 + ".zip"
	second := "_ray_pkg_" + testRayPackageSHA1 + ".zip"
	if response := rayRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+first, payload); response.Code != http.StatusOK {
		t.Fatalf("first package status=%d body=%s", response.Code, response.Body.String())
	}
	if response := rayRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+second, payload); response.Code != http.StatusConflict || strings.Contains(response.Body.String(), first) || strings.Contains(response.Body.String(), second) {
		t.Fatalf("alias conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRayRoutesEnforcePATScopesAndTenantIsolation(t *testing.T) {
	packageName := testPackageSHA256 + ".zip"
	readOnly := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}
	if response := rayRequest(rayRouter(t, &rayTestRepository{}, &rayTestStore{}, readOnly), http.MethodPut, "/ray/api/packages/gcs/"+packageName, "payload"); response.Code != http.StatusForbidden {
		t.Fatalf("read-only PAT upload status=%d", response.Code)
	}

	repository := &rayTestRepository{}
	writer := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	router := rayRouter(t, repository, &rayTestStore{}, writer)
	if response := rayRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, "payload"); response.Code != http.StatusOK {
		t.Fatalf("owner upload status=%d", response.Code)
	}
	other := auth.Principal{Subject: "user-b", TenantID: "tenant-b", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	if response := rayRequest(rayRouter(t, repository, &rayTestStore{}, other), http.MethodGet, "/ray/api/packages/gcs/"+packageName, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant package lookup status=%d body=%s", response.Code, response.Body.String())
	}
}
