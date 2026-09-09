package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOAuth2ProxyPrincipalUsesKeycloakGroups(t *testing.T) {
	headers := make(http.Header)
	headers.Set(oauth2ProxyUserHeader, "8cbe4c")
	headers.Set(oauth2ProxyUsernameHeader, "alice")
	headers.Set(oauth2ProxyEmailHeader, "alice@example.com")
	headers.Set(oauth2ProxyGroupsHeader, "/platform/tenants/local, /platform/roles/TenantAdmin")

	principal, err := OAuth2ProxyPrincipal(headers, "platform/tenants/")
	if err != nil {
		t.Fatalf("OAuth2ProxyPrincipal() error = %v", err)
	}
	if principal.Subject != "8cbe4c" || principal.Username != "alice" || principal.TenantID != "local" || !principal.HasRole("TenantAdmin") || principal.AuthType != AuthTypeOAuth2Proxy {
		t.Fatalf("unexpected principal: %#v", principal)
	}
}

func TestOAuth2ProxyMiddlewareRejectsBrowserBearerAndMissingRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, headers := range []http.Header{
		{"Authorization": []string{"Bearer browser-jwt"}},
		{oauth2ProxyUserHeader: []string{"alice"}, oauth2ProxyGroupsHeader: []string{"platform/tenants/local"}},
	} {
		router := gin.New()
		router.Use(OAuth2ProxyMiddleware(nil, true, "platform/tenants/"))
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
