package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestPublicTrainingRouteRegistrarRejectsReadOnlyPATSubmit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{
			Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"},
			AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead},
		})
		c.Next()
	})
	handler := NewHandler(&fakeJobRepository{}, Options{})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("public registrar allowed read-only PAT to reach submit handler: status=%d", response.Code)
	}
}
