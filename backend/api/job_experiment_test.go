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
	"ray-train-platform-backend/observability"
)

type fakeExperimentProvider struct {
	tenant  string
	jobID   string
	subject string
	limit   int
	catalog observability.ExperimentCatalog
}

func (provider *fakeExperimentProvider) QueryJobExperiment(_ context.Context, tenant, jobID string) (observability.JobExperiment, error) {
	provider.tenant, provider.jobID = tenant, jobID
	return observability.JobExperiment{ExperimentName: "raytrain-" + tenant}, nil
}

func (provider *fakeExperimentProvider) ListTenantExperiments(_ context.Context, tenant, subject string, limit int) (observability.ExperimentCatalog, error) {
	provider.tenant, provider.subject, provider.limit = tenant, subject, limit
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
