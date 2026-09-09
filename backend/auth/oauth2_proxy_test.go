package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

type fakeOAuth2ProxyAccountResolver struct {
	user  domain.LocalUser
	found bool
	err   error
	calls int
}

func (r *fakeOAuth2ProxyAccountResolver) ResolveOAuth2ProxyAccount(_ context.Context, _ string) (domain.LocalUser, bool, error) {
	r.calls++
	return r.user, r.found, r.err
}

func TestOAuth2ProxyMiddlewareRequiresVerifiedProxyAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, headers := range []http.Header{
		{"Authorization": []string{"Bearer browser-jwt"}},
		{"X-Auth-Request-User": []string{"alice"}, "X-Auth-Request-Groups": []string{"platform/tenants/local,roles/Engineer"}},
		{oauth2ProxyAccessTokenHeader: []string{"invalid-jwt"}},
	} {
		router := gin.New()
		router.Use(OAuth2ProxyMiddleware(&fakeOIDCVerifier{err: errors.New("invalid token")}, &fakeOAuth2ProxyAccountResolver{}, nil, nil, true))
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
	for _, tokenSource := range []string{"xauth-header", "legacy-browser-bearer"} {
		t.Run(tokenSource, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			verifier := &fakeOIDCVerifier{principal: Principal{
				Subject: "keycloak-subject-1", Username: "alice", Email: "alice@example.com",
			}}
			resolver := &fakeOAuth2ProxyAccountResolver{found: true, user: domain.LocalUser{
				ID: "platform-user-1", Username: "alice", Email: "platform-alice@example.com",
				TenantID: "local", Roles: []string{"TenantAdmin"},
			}}
			router := gin.New()
			router.Use(OAuth2ProxyMiddleware(verifier, resolver, nil, nil, true))
			router.GET("/", func(c *gin.Context) {
				principal, ok := PrincipalFromGin(c)
				if !ok || principal.AuthType != AuthTypeOAuth2Proxy || principal.Subject != "platform-user-1" ||
					principal.TenantID != "local" || principal.Email != "platform-alice@example.com" || !principal.HasRole("TenantAdmin") {
					c.Status(http.StatusInternalServerError)
					return
				}
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if tokenSource == "xauth-header" {
				request.Header.Set(oauth2ProxyAccessTokenHeader, "signed-jwt")
			} else {
				request.Header.Set("Authorization", "Bearer signed-jwt")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || verifier.calls != 1 || resolver.calls != 1 {
				t.Fatalf("verified proxy identity was not accepted: status=%d verifier=%d resolver=%d body=%s", response.Code, verifier.calls, resolver.calls, response.Body.String())
			}
		})
	}
}

func TestOAuth2ProxyMiddlewareRejectsUnprovisionedOrDisabledAccounts(t *testing.T) {
	tests := []struct {
		name     string
		resolver *fakeOAuth2ProxyAccountResolver
		want     int
	}{
		{name: "not provisioned", resolver: &fakeOAuth2ProxyAccountResolver{}, want: http.StatusForbidden},
		{name: "disabled", resolver: &fakeOAuth2ProxyAccountResolver{found: true, user: domain.LocalUser{ID: "u1", Username: "alice", TenantID: "local", Roles: []string{"Engineer"}, Disabled: true}}, want: http.StatusForbidden},
		{name: "lookup unavailable", resolver: &fakeOAuth2ProxyAccountResolver{err: errors.New("database unavailable")}, want: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(OAuth2ProxyMiddleware(
				&fakeOIDCVerifier{principal: Principal{Subject: "keycloak-subject-1", Username: "alice"}},
				test.resolver, nil, nil, true,
			))
			router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(oauth2ProxyAccessTokenHeader, "signed-jwt")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d, want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestOAuth2ProxyMiddlewarePreservesLocalSessionDuringMigration(t *testing.T) {
	issued := issuedTestSession(t)
	authenticator, err := NewLocalSessionAuthenticator(localStoreFor(issued), testAuthPepper(), nil)
	if err != nil {
		t.Fatalf("new local authenticator: %v", err)
	}

	response := serveMiddleware(t, OAuth2ProxyMiddleware(nil, nil, nil, authenticator, true), "Bearer "+issued.Token)
	if response.Code != http.StatusNoContent {
		t.Fatalf("local migration session was not accepted: status=%d body=%s", response.Code, response.Body.String())
	}
}
