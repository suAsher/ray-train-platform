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

type fakeOIDCVerifier struct {
	principal Principal
	err       error
	calls     int
}

func (v *fakeOIDCVerifier) Verify(_ context.Context, _ string) (Principal, error) {
	v.calls++
	return v.principal, v.err
}

type fakePATVerifier struct {
	identity PATIdentity
	err      error
	calls    int
}

func (v *fakePATVerifier) Authenticate(_ context.Context, _ string) (PATIdentity, error) {
	v.calls++
	return v.identity, v.err
}

func serveMiddleware(t *testing.T, middleware gin.HandlerFunc, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware)
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestHybridMiddlewareNeverDowngradesPATToOIDC(t *testing.T) {
	oidc := &fakeOIDCVerifier{principal: Principal{Subject: "oidc-user", TenantID: "tenant-a"}}
	pat := &fakePATVerifier{err: ErrInvalidPAT}

	response := serveMiddleware(t, HybridMiddleware(oidc, pat, true), "Bearer rpt_invalid")

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", response.Code, response.Body.String())
	}
	if pat.calls != 1 || oidc.calls != 0 {
		t.Fatalf("PAT must not fall back to OIDC, pat=%d oidc=%d", pat.calls, oidc.calls)
	}
}

func TestHybridMiddlewareRoutesOIDCTokensOnlyToOIDC(t *testing.T) {
	oidc := &fakeOIDCVerifier{principal: Principal{Subject: "oidc-user", TenantID: "tenant-a"}}
	pat := &fakePATVerifier{identity: PATIdentity{Principal: Principal{Subject: "pat-user", TenantID: "tenant-a"}}}

	response := serveMiddleware(t, HybridMiddleware(oidc, pat, true), "Bearer jwt-token")

	if response.Code != http.StatusNoContent || oidc.calls != 1 || pat.calls != 0 {
		t.Fatalf("unexpected OIDC dispatch status=%d oidc=%d pat=%d", response.Code, oidc.calls, pat.calls)
	}
}

func TestHybridMiddlewareClassifiesPATInfrastructureFailureAsUnavailable(t *testing.T) {
	pat := &fakePATVerifier{err: errors.New("database unavailable")}
	response := serveMiddleware(t, HybridMiddleware(&fakeOIDCVerifier{}, pat, true), "Bearer rpt_token")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", response.Code, response.Body.String())
	}
}

func TestHybridMiddlewareOptionalAuthenticationOnlyAllowsMissingHeader(t *testing.T) {
	middleware := HybridMiddleware(&fakeOIDCVerifier{err: errors.New("bad JWT")}, &fakePATVerifier{err: ErrInvalidPAT}, false)
	if response := serveMiddleware(t, middleware, ""); response.Code != http.StatusNoContent {
		t.Fatalf("missing optional header should pass, got %d", response.Code)
	}
	if response := serveMiddleware(t, middleware, "Basic bad"); response.Code != http.StatusUnauthorized {
		t.Fatalf("malformed optional header must fail, got %d", response.Code)
	}
	if response := serveMiddleware(t, middleware, "Bearer bad-jwt"); response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid optional credential must fail, got %d", response.Code)
	}
}

func TestHybridMiddlewareStoresAuthenticationTypeAndScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pat := &fakePATVerifier{identity: PATIdentity{
		Principal: Principal{Subject: "pat-user", TenantID: "tenant-a", Roles: []string{"Engineer"}},
		Scopes:    []string{domain.PATScopeJobsRead},
	}}
	router := gin.New()
	router.Use(HybridMiddleware(&fakeOIDCVerifier{}, pat, true))
	router.GET("/test", func(c *gin.Context) {
		principal, ok := PrincipalFromGin(c)
		if !ok || principal.AuthType != AuthTypePAT || !principal.HasScope(domain.PATScopeJobsRead) {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	request.Header.Set("Authorization", "Bearer rpt_token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("PAT identity was not stored correctly: %d", response.Code)
	}
}

func TestRequireScopesEnforcesPATScopesAndTrustsOIDCBusinessAccess(t *testing.T) {
	tests := []struct {
		name      string
		principal *Principal
		want      int
	}{
		{name: "missing principal", want: http.StatusUnauthorized},
		{name: "PAT missing scope", principal: &Principal{Subject: "pat", AuthType: AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, want: http.StatusForbidden},
		{name: "PAT has every scope", principal: &Principal{Subject: "pat", AuthType: AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead, domain.PATScopeJobsWrite}}, want: http.StatusNoContent},
		{name: "OIDC uses roles instead", principal: &Principal{Subject: "oidc", AuthType: AuthTypeOIDC}, want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			if test.principal != nil {
				principal := *test.principal
				router.Use(func(c *gin.Context) { setPrincipal(c, principal); c.Next() })
			}
			router.Use(RequireScopes(domain.PATScopeJobsRead, domain.PATScopeJobsWrite))
			router.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d: %s", test.want, response.Code, response.Body.String())
			}
		})
	}
}

func TestOIDCOnlyRejectsPATAndAllowsConfiguredDemoPrincipal(t *testing.T) {
	pat := Principal{Subject: "pat", TenantID: "tenant-a", AuthType: AuthTypePAT}
	if response := servePrincipalMiddleware(t, pat, OIDCOnly(true)); response.Code != http.StatusForbidden {
		t.Fatalf("PAT must not access OIDC-only routes, got %d", response.Code)
	}
	demo := Principal{Subject: "local-user", TenantID: "local", AuthType: AuthTypeDemo}
	if response := servePrincipalMiddleware(t, demo, OIDCOnly(true)); response.Code != http.StatusNoContent {
		t.Fatalf("configured demo principal should pass, got %d", response.Code)
	}
	if response := servePrincipalMiddleware(t, demo, OIDCOnly(false)); response.Code != http.StatusForbidden {
		t.Fatalf("demo principal must be rejected outside demo mode, got %d", response.Code)
	}
}

func servePrincipalMiddleware(t *testing.T, principal Principal, middleware gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { setPrincipal(c, principal); c.Next() }, middleware)
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
	return response
}
