package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

type fakeExperimentProvider struct {
	tenant  string
	jobID   string
	subject string
	limit   int
	catalog observability.ExperimentCatalog
	catalogs map[string]observability.ExperimentCatalog
	queried []string
}

func (provider *fakeExperimentProvider) QueryJobExperiment(_ context.Context, tenant, jobID string) (observability.JobExperiment, error) {
	provider.tenant, provider.jobID = tenant, jobID
	return observability.JobExperiment{ExperimentName: "raytrain-" + tenant}, nil
}

func (provider *fakeExperimentProvider) ListTenantExperiments(_ context.Context, tenant, subject string, limit int) (observability.ExperimentCatalog, error) {
	provider.tenant, provider.subject, provider.limit = tenant, subject, limit
	provider.queried = append(provider.queried, tenant)
	if provider.catalogs != nil {
		return provider.catalogs[tenant], nil
	}
	return provider.catalog, nil
}

func TestGetJobExperimentUsesAuthenticatedTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-a"}, {ID: "job-01", TenantID: "team-b", UserID: "user-b"}}}
	provider := &fakeExperimentProvider{}
	handler := NewHandler(repository, Options{Experiments: provider})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/experiment", nil))
	if response.Code != http.StatusOK || provider.tenant != "team-a" || provider.jobID != "job-01" {
		t.Fatalf("unexpected scoped response: status=%d tenant=%q job=%q body=%s", response.Code, provider.tenant, provider.jobID, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "team-b") {
		t.Fatalf("cross-tenant data leaked: %s", response.Body.String())
	}
}

func TestGetJobExperimentReportsDisabledIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a"}}}
	handler := NewHandler(repository, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/experiment", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "MLFLOW_UNAVAILABLE") {
		t.Fatalf("unexpected disabled response: %d %s", response.Code, response.Body.String())
	}
}

func TestGetJobExperimentRejectsAnotherEngineerInTheSameTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-b"}}}
	handler := NewHandler(repository, Options{Experiments: &fakeExperimentProvider{}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/experiment", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("same-tenant cross-owner experiment must be forbidden: %d %s", response.Code, response.Body.String())
	}
}

func TestListExperimentsScopesEngineerToAuthenticatedSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &fakeExperimentProvider{catalog: observability.ExperimentCatalog{ExperimentName: "raytrain-team-a"}}
	handler := NewHandler(&fakeJobRepository{}, Options{Experiments: provider})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/experiments?limit=25", nil))
	if response.Code != http.StatusOK || provider.tenant != "team-a" || provider.subject != "user-a" || provider.limit != 25 {
		t.Fatalf("unexpected scoped catalog request: status=%d tenant=%q subject=%q limit=%d body=%s", response.Code, provider.tenant, provider.subject, provider.limit, response.Body.String())
	}
}

func TestListExperimentsLetsTenantAdminSeeTenantCatalogAndClampsLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &fakeExperimentProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Experiments: provider})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "admin-a", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/experiments?limit=999", nil))
	if response.Code != http.StatusOK || provider.tenant != "team-a" || provider.subject != "" || provider.limit != 100 {
		t.Fatalf("unexpected admin catalog request: status=%d tenant=%q subject=%q limit=%d body=%s", response.Code, provider.tenant, provider.subject, provider.limit, response.Body.String())
	}
}

func TestListExperimentsReportsDisabledIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&fakeJobRepository{}, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/experiments", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "MLFLOW_UNAVAILABLE") {
		t.Fatalf("unexpected disabled response: %d %s", response.Code, response.Body.String())
	}
}

func TestListExperimentsDropsForgedOrCrossOwnerMLflowTags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{
		{ID: "owned", TenantID: "team-a", UserID: "user-a"},
		{ID: "other", TenantID: "team-a", UserID: "user-b"},
	}}
	provider := &fakeExperimentProvider{catalog: observability.ExperimentCatalog{
		ExperimentName: "raytrain-team-a",
		Runs: []observability.ExperimentRunSummary{
			{ID: "run-owned", JobID: "owned", SubmitterUserID: "user-a"},
			{ID: "run-forged-owner", JobID: "other", SubmitterUserID: "user-a"},
			{ID: "run-missing-job", JobID: "missing", SubmitterUserID: "user-a"},
		},
	}}
	handler := NewHandler(repository, Options{Experiments: provider})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/experiments", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "run-owned") || strings.Contains(response.Body.String(), "run-forged-owner") || strings.Contains(response.Body.String(), "run-missing-job") {
		t.Fatalf("catalog did not fail closed against forged MLflow tags: %d %s", response.Code, response.Body.String())
	}
}

func TestListExperimentsSuperAdminMergesAllTenantsBeforeApplyingLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	admin := &fakeAdminStore{}
	provider := &fakeExperimentProvider{catalogs: map[string]observability.ExperimentCatalog{}}
	// The newest run belongs to the final tenant. Limiting tenant/job discovery
	// to the response size (or even 100) must not omit it.
	for index := 0; index < 105; index++ {
		tenant := fmt.Sprintf("team-%03d", index)
		jobID := fmt.Sprintf("job-%03d", index)
		admin.tenants = append(admin.tenants, repositories.TenantSummary{ID: tenant})
		repository.jobs = append(repository.jobs, domain.TrainingJob{ID: jobID, TenantID: tenant, UserID: "db-owner"})
		provider.catalogs[tenant] = observability.ExperimentCatalog{Runs: []observability.ExperimentRunSummary{
			{ID: "run-" + jobID, JobID: jobID, SubmitterUserID: "forged-owner", StartTimeMS: int64(index)},
			{ID: "missing-" + jobID, JobID: "missing", StartTimeMS: 9999},
		}}
	}
	// A copied tag from another tenant must not attach a run to that tenant's job.
	provider.catalogs["team-000"] = observability.ExperimentCatalog{Runs: []observability.ExperimentRunSummary{
		{ID: "cross-tenant-forged", JobID: "job-104", StartTimeMS: 99999},
	}}
	handler := NewHandler(repository, Options{Experiments: provider, Admin: admin})
	response := requestExperimentCatalog(handler, auth.Principal{Subject: "root", TenantID: "team-000", Roles: []string{domain.RoleSuperAdmin}}, "?limit=2")
	var payload struct { Data observability.ExperimentCatalog `json:"data"` }
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil { t.Fatal(err) }
	if response.Code != http.StatusOK || len(payload.Data.Runs) != 2 || payload.Data.Runs[0].JobID != "job-104" || payload.Data.Runs[1].JobID != "job-103" {
		t.Fatalf("global catalog must sort all tenants then limit: %d %s", response.Code, response.Body.String())
	}
	if len(provider.queried) != 105 || provider.subject != "" || provider.limit != 2 {
		t.Fatalf("global catalog omitted tenants or changed requested limit: %+v", provider)
	}
	for _, run := range payload.Data.Runs {
		if run.SubmitterUserID != "db-owner" { t.Fatalf("owner must come from database: %+v", run) }
	}
}

func TestListExperimentsTenantAdminCannotReadOtherTenantCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &fakeExperimentProvider{catalogs: map[string]observability.ExperimentCatalog{
		"team-a": {Runs: []observability.ExperimentRunSummary{{ID: "own-team", JobID: "job-a"}, {ID: "forged-other-team", JobID: "job-b"}}},
		"team-b": {Runs: []observability.ExperimentRunSummary{{ID: "other-team", JobID: "job-b"}}},
	}}
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-a", TenantID: "team-a", UserID: "other-member"}, {ID: "job-b", TenantID: "team-b", UserID: "admin-a"}}}
	handler := NewHandler(repository, Options{Experiments: provider, Admin: &fakeAdminStore{tenants: []repositories.TenantSummary{{ID: "team-a"}, {ID: "team-b"}}}})
	response := requestExperimentCatalog(handler, auth.Principal{Subject: "admin-a", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}}, "?tenantId=team-b&scope=all")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "own-team") || strings.Contains(response.Body.String(), "other-team") || len(provider.queried) != 1 || provider.queried[0] != "team-a" {
		t.Fatalf("tenant admin crossed tenant boundary: %d %s queries=%v", response.Code, response.Body.String(), provider.queried)
	}
}

func requestExperimentCatalog(handler *Handler, principal auth.Principal, query string) *httptest.ResponseRecorder {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal); c.Next() })
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/experiments"+query, nil))
	return response
}
