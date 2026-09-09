package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOAuth2ProxyMiddlewareRequiresVerifiedProxyAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, headers := range []http.Header{
		{"Authorization": []string{"Bearer browser-jwt"}},
		{"X-Auth-Request-User": []string{"alice"}, "X-Auth-Request-Groups": []string{"platform/tenants/local,roles/Engineer"}},
		{oauth2ProxyAccessTokenHeader: []string{"invalid-jwt"}},
	} {
		router := gin.New()
		router.Use(OAuth2ProxyMiddleware(&fakeOIDCVerifier{err: errors.New("invalid token")}, nil, true))
		router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header = headers
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
		}
	}
}

func TestOAuth2ProxyMiddlewareBuildsInteractivePrincipalFromVerifiedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	verifier := &fakeOIDCVerifier{principal: Principal{
		Subject: "subject-1", Username: "alice", Email: "alice@example.com",
		TenantID: "local", Roles: []string{"TenantAdmin"},
	}}
	router := gin.New()
	router.Use(OAuth2ProxyMiddleware(verifier, nil, true))
	router.GET("/", func(c *gin.Context) {
		principal, ok := PrincipalFromGin(c)
		if !ok || principal.AuthType != AuthTypeOAuth2Proxy || principal.Subject != "subject-1" || !principal.HasRole("TenantAdmin") {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(oauth2ProxyAccessTokenHeader, "signed-jwt")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || verifier.calls != 1 {
		t.Fatalf("verified proxy identity was not accepted: status=%d calls=%d body=%s", response.Code, verifier.calls, response.Body.String())
	}
}
