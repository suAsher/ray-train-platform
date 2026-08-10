package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOptionalHybridMiddlewareExecutesDownstreamOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := 0
	router := gin.New()
	router.Use(HybridMiddleware(nil, nil, false))
	router.GET("/test", func(c *gin.Context) {
		calls++
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
	if response.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("optional middleware status=%d downstream calls=%d", response.Code, calls)
	}
}

func TestHybridMiddlewareTreatsTypedNilOIDCVerifierAsInvalidCredentials(t *testing.T) {
	var disabled *Validator
	response := serveMiddleware(t, HybridMiddleware(disabled, nil, true), "Bearer jwt-token")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled OIDC authentication expected 401, got %d", response.Code)
	}
}
