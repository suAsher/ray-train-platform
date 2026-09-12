package auth

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIntegrationRouteGuardUsesClosedMethodAndPathAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/v1/me", true}, {"GET", "/api/v1/mlflow/capabilities", true},
		{"POST", "/api/v1/mlflow/experiments", true}, {"GET", "/api/v1/mlflow/experiments/0123456789abcdef0123456789abcdef/runs", true},
		{"POST", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/log-batch", true},
		{"GET", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/get", true},
		{"POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/log-metric", true},
		{"GET", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts", true},
		{"POST", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts", true},
		{"GET", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/content", true},
		{"DELETE", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"POST", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/complete", true},
		{"PUT", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/parts/2560", true},
		{"PUT", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/parts/2561", false},
		{"PUT", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/parts/0", false},
		{"PUT", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/parts/01", false},
		{"GET", "/api/v1/mlflow/runs/0123456789abcdef0123456789abcdef/artifacts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/other", false},
		{"GET", "/api/v1/jobs", false}, {"POST", "/api/v1/personal-access-tokens", false},
		{"GET", "/api/v1/mlflow/integrations", false}, {"POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/create", false},
		{"GET", "/api/v1/mlflow/experiments/../../jobs", false}, {"DELETE", "/api/v1/mlflow/experiments", false},
		{"GET", "/api/v1/mlflow/anything", false}, {"GET", "/api/v1/mlflow/runs/not-an-id", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				setPrincipal(c, Principal{Subject: "integration:abc", IntegrationID: "abc", AuthType: AuthTypePAT})
				c.Next()
			}, IntegrationRouteGuard())
			r.NoRoute(func(c *gin.Context) { c.Status(http.StatusNoContent) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			want := http.StatusForbidden
			if tc.allowed {
				want = http.StatusNoContent
			}
			if w.Code != want {
				t.Fatalf("got %d want %d", w.Code, want)
			}
		})
	}
}

func TestIntegrationRouteGuardDoesNotChangeHumanAccess(t *testing.T) {
	r := gin.New()
	r.Use(func(c *gin.Context) { setPrincipal(c, Principal{Subject: "human", AuthType: AuthTypePAT}); c.Next() }, IntegrationRouteGuard())
	r.GET("/api/v1/jobs", func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/jobs", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
