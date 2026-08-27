package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestCheckpointListExcludesAnotherUsersJobForEngineer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	job := domain.TrainingJob{ID: "job-a", TenantID: "tenant-a", UserID: "user-b"}
	store := &fakeManagedTrainingStore{fakeJobRepository: &fakeJobRepository{jobs: []domain.TrainingJob{job}}, items: []domain.TrainingCheckpoint{{ID: "checkpoint-1", Complete: true}}}
	handler := NewHandler(store, Options{})
	router := gin.New()
	router.Use(checkpointPrincipalMiddleware(auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}))
	handler.RegisterCheckpointRoutes(router.Group("/api/v1"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/checkpoints", nil))
	if response.Code != http.StatusNotFound || store.wantJobID != "" {
		t.Fatalf("foreign user checkpoints leaked: code=%d body=%s query=%q", response.Code, response.Body.String(), store.wantJobID)
	}
}

func TestCheckpointListScopesOwnerTenantAdminAndSuperAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	job := domain.TrainingJob{ID: "job-a", TenantID: "tenant-a", UserID: "user-a"}
	principals := []auth.Principal{
		{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC},
		{Subject: "admin-a", TenantID: "tenant-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeOIDC},
		{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeOIDC},
	}
	for _, principal := range principals {
		store := &fakeManagedTrainingStore{fakeJobRepository: &fakeJobRepository{jobs: []domain.TrainingJob{job}}, items: []domain.TrainingCheckpoint{{ID: "checkpoint-1", JobID: job.ID, TenantID: job.TenantID, UserID: job.UserID, Complete: true}}}
		handler := NewHandler(store, Options{})
		router := gin.New()
		router.Use(checkpointPrincipalMiddleware(principal))
		handler.RegisterCheckpointRoutes(router.Group("/api/v1"))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/checkpoints", nil))
		if response.Code != http.StatusOK || store.wantJobID != "tenant-a/user-a/job-a" {
			t.Fatalf("principal %+v was not scoped to job owner: code=%d body=%s query=%q", principal, response.Code, response.Body.String(), store.wantJobID)
		}
	}
}

func checkpointPrincipalMiddleware(principal auth.Principal) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	}
}
