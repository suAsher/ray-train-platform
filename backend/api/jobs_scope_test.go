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

func TestTrainingRoutesApplyReadAndWritePATScopes(t *testing.T) {
	tests := []struct {
		name      string
		principal auth.Principal
		method    string
		path      string
		body      string
		want      int
	}{
		{name: "read PAT can list", principal: auth.Principal{Subject: "user", TenantID: "tenant", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, method: http.MethodGet, path: "/api/v1/jobs", want: http.StatusOK},
		{name: "read PAT cannot submit", principal: auth.Principal{Subject: "user", TenantID: "tenant", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, method: http.MethodPost, path: "/api/v1/jobs", body: `{}`, want: http.StatusForbidden},
		{name: "write PAT cannot list", principal: auth.Principal{Subject: "user", TenantID: "tenant", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}, method: http.MethodGet, path: "/api/v1/jobs", want: http.StatusForbidden},
		{name: "write PAT reaches submit validation", principal: auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}, method: http.MethodPost, path: "/api/v1/jobs", body: `{}`, want: http.StatusBadRequest},
		{name: "write PAT reaches preflight validation", principal: auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}, method: http.MethodPost, path: "/api/v1/jobs/preflight", body: `{}`, want: http.StatusBadRequest},
		{name: "OIDC uses role authorization", principal: auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}, method: http.MethodPost, path: "/api/v1/jobs", body: `{}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			principal := test.principal
			router.Use(func(c *gin.Context) {
				c.Set("ray-platform-principal", principal)
				c.Next()
			})
			handler := NewHandler(&fakeJobRepository{}, Options{})
			handler.RegisterTrainingRoutes(router.Group("/api/v1"))
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d", test.want, response.Code)
			}
		})
	}
}
