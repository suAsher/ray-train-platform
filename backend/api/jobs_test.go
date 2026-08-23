package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type fakeJobRepository struct {
	created     *domain.TrainingJob
	jobs        []domain.TrainingJob
	identity    int
	identityErr error
	canceled    string
}

func (f *fakeJobRepository) Create(_ context.Context, job *domain.TrainingJob, _ string) error {
	f.created = job
	f.jobs = append(f.jobs, *job)
	return nil
}
func (f *fakeJobRepository) Get(_ context.Context, tenant, id string) (*domain.TrainingJob, error) {
	for _, job := range f.jobs {
		if job.ID == id && job.TenantID == tenant {
			copy := job
			return &copy, nil
		}
	}
	return nil, context.Canceled
}
func (f *fakeJobRepository) GetByID(_ context.Context, id string) (*domain.TrainingJob, error) {
	for _, job := range f.jobs {
		if job.ID == id {
			copy := job
			return &copy, nil
		}
	}
	return nil, context.Canceled
}
func (f *fakeJobRepository) List(_ context.Context, filter domain.JobFilter) (domain.Page[domain.TrainingJob], error) {
	items := make([]domain.TrainingJob, 0)
	for _, job := range f.jobs {
		if filter.AllTenants || job.TenantID == filter.TenantID {
			items = append(items, job)
		}
	}
	return domain.Page[domain.TrainingJob]{Items: items, Total: int64(len(items))}, nil
}
func (f *fakeJobRepository) SetDesiredState(_ context.Context, tenant, id string, state domain.DesiredState) error {
	f.canceled = tenant + "/" + id + "/" + string(state)
	return nil
}
func (f *fakeJobRepository) EnsureIdentity(context.Context, auth.Principal) error {
	f.identity++
	return f.identityErr
}

func TestSubmitJobUsesAuthenticatedTenantAndQueuesOutboxViaRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	handler.newID = func() (string, error) { return "job-fixed", nil }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"train-one","image":"registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1},"queue":""}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d", response.Code)
	}
	if repository.created == nil || repository.created.TenantID != "team-a" || repository.created.Spec.Queue != "team-a-gpu" {
		t.Fatalf("unexpected created job: %+v", repository.created)
	}
	if repository.identity != 1 {
		t.Fatalf("expected identity upsert")
	}
}

func TestTenantAdminSubmissionPersistsPrincipalIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	handler.newID = func() (string, error) { return "job-tenant-admin", nil }
	principal := auth.Principal{
		Subject:  "user-tenant-admin-42",
		Username: "team-admin-client-name",
		TenantID: "team-a",
		Roles:    []string{domain.RoleTenantAdmin},
		AuthType: auth.AuthTypeLocal,
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"tenant-admin-job","image":"registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d: %s", response.Code, response.Body.String())
	}
	if repository.created == nil {
		t.Fatal("expected a persisted job")
	}
	if repository.created.UserID != principal.Subject || repository.created.TenantID != principal.TenantID {
		t.Fatalf("job identity must come from authenticated subject and tenant, got user=%q tenant=%q", repository.created.UserID, repository.created.TenantID)
	}
	if repository.created.UserID == principal.Username || repository.created.UserID == domain.RoleTenantAdmin || repository.created.UserID == "admin" || repository.created.UserID == "client" {
		t.Fatalf("job submitter must never use role, admin, client, or username labels: %q", repository.created.UserID)
	}
}

func TestSubmitJobRejectsAnotherTenantQueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"train-one","image":"registry.example/ray-train@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1},"queue":"team-b-gpu"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || repository.created != nil {
		t.Fatalf("expected queue rejection, got %d", response.Code)
	}
}

func TestSubmitJobRecordsPortalSubmissionOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	handler.newID = func() (string, error) { return "job-portal", nil }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"portal-job","image":"registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d", response.Code)
	}
	if repository.created == nil || repository.created.SubmissionOrigin != domain.SubmissionOriginPortal {
		t.Fatalf("expected portal origin, got %+v", repository.created)
	}
}

func TestSubmitJobRecordsRayCLIOriginWhenRequested(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	handler.newID = func() (string, error) { return "job-spk-rayjob", nil }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"origin":"ray-cli","spec":{"name":"spk-rayjob-job","image":"registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d", response.Code)
	}
	if repository.created == nil || repository.created.SubmissionOrigin != domain.SubmissionOriginRayCLI {
		t.Fatalf("expected spk-rayjob origin, got %+v", repository.created)
	}
}

func TestPublicTrainingJobRedactsResolvedPVCClaims(t *testing.T) {
	job := &domain.TrainingJob{Spec: domain.JobSpec{
		DatasetStorage: domain.StorageSelection{AssetID: "dataset-a", RelativePath: "train"},
		ResolvedStorage: domain.ResolvedStorageMounts{
			Dataset: &domain.ResolvedStorageMount{AssetID: "dataset-a", ClaimName: "tos-private-claim", MountPath: domain.StorageMountDataset, ReadOnly: true},
		},
	}}
	public := publicTrainingJob(job)
	if public == job || public.Spec.DatasetStorage.AssetID != "dataset-a" || public.Spec.ResolvedStorage.Dataset != nil {
		t.Fatalf("public job did not preserve selection and redact renderer details: %#v", public)
	}
	if job.Spec.ResolvedStorage.Dataset == nil || job.Spec.ResolvedStorage.Dataset.ClaimName != "tos-private-claim" {
		t.Fatalf("redaction mutated the persisted job: %#v", job)
	}
}

func TestSuperAdminListsJobsAcrossTenantBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{
		{ID: "job-team-a", TenantID: "team-a", Spec: domain.JobSpec{Name: "team-a-job"}},
		{ID: "job-team-b", TenantID: "team-b", Spec: domain.JobSpec{Name: "team-b-job"}},
	}}
	handler := NewHandler(repository, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "job-team-a") || !strings.Contains(response.Body.String(), "job-team-b") {
		t.Fatalf("super administrator must see every tenant job, got %d: %s", response.Code, response.Body.String())
	}
}

func TestSuperAdminCanReadAndCancelCrossTenantJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-team-b", TenantID: "team-b", Spec: domain.JobSpec{Name: "team-b-job"}}}}
	handler := NewHandler(repository, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-team-b", nil))
	if readResponse.Code != http.StatusOK || !strings.Contains(readResponse.Body.String(), "job-team-b") {
		t.Fatalf("super administrator must read a cross-tenant job, got %d: %s", readResponse.Code, readResponse.Body.String())
	}

	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/job-team-b", nil))
	if cancelResponse.Code != http.StatusAccepted || repository.canceled != "team-b/job-team-b/CANCELED" {
		t.Fatalf("super administrator cancellation must target the owning tenant, got %d canceled=%q body=%s", cancelResponse.Code, repository.canceled, cancelResponse.Body.String())
	}
}

func TestEngineerCannotSelectAnotherTenantJobList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-team-b", TenantID: "team-b"}}}
	handler := NewHandler(repository, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs?tenantId=team-b", nil))

	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "TENANT_SCOPE_FORBIDDEN") {
		t.Fatalf("engineer must not select another tenant, got %d: %s", response.Code, response.Body.String())
	}
}
