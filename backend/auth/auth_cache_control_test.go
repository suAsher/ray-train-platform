package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

func TestAuthenticationAndScopeFailuresDisableCaching(t *testing.T) {
	unauthorized := serveMiddleware(t, HybridMiddleware(nil, nil, true), "")
	assertAuthenticationNoStore(t, unauthorized, http.StatusUnauthorized)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		setPrincipal(c, Principal{Subject: "pat", AuthType: AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}})
		c.Next()
	}, RequireScopes(domain.PATScopeSourcesWrite))
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	forbidden := httptest.NewRecorder()
	router.ServeHTTP(forbidden, httptest.NewRequest(http.MethodGet, "/test", nil))
	assertAuthenticationNoStore(t, forbidden, http.StatusForbidden)
}

func assertAuthenticationNoStore(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d, want %d", response.Code, status)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("authentication failure is cacheable: headers=%v", response.Header())
	}
}
