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

func TestDelegationUsesOnlyTokenThatAuthenticatedTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name, bearer, header, want string
		invalid                    bool
	}{
		{name: "proxy", header: "verified-proxy", want: "verified-proxy"},
		{name: "bearer precedence", bearer: "verified-bearer", header: "unverified-header", want: "verified-bearer"},
		{name: "invalid", header: "invalid", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &fakeOIDCVerifier{principal: Principal{Subject: "oidc-user", Username: "alice"}}
			if tc.invalid {
				verifier.err = errors.New("invalid token")
			}
			resolver := &fakeOAuth2ProxyAccountResolver{found: true, user: domain.LocalUser{ID: "alice", Username: "alice", TenantID: "local", Roles: []string{"Engineer"}}}
			r := gin.New()
			var token string
			r.Use(func(c *gin.Context) {
				c.Next()
				token, _ = VerifiedOAuth2AccessToken(c.Request.Context())
			})
			r.Use(OAuth2ProxyMiddleware(verifier, resolver, nil, nil, true, OAuth2ProxyOptions{}))
			r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(oauth2ProxyAccessTokenHeader, tc.header)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if token != tc.want {
				t.Fatal("delegated token does not match verified authentication source")
			}
			if tc.invalid && w.Code != 401 {
				t.Fatalf("invalid token accepted: %d", w.Code)
			}
		})
	}
}

func TestDelegationAbsentWithoutOAuthVerification(t *testing.T) {
	if _, ok := VerifiedOAuth2AccessToken(context.Background()); ok {
		t.Fatal("unverified context has delegated token")
	}
	issued := issuedTestSession(t)
	authenticator, err := NewLocalSessionAuthenticator(localStoreFor(issued), testAuthPepper(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(OAuth2ProxyMiddleware(nil, nil, nil, authenticator, true, OAuth2ProxyOptions{}))
	r.GET("/", func(c *gin.Context) {
		if _, ok := VerifiedOAuth2AccessToken(c.Request.Context()); ok {
			t.Error("local session delegated unverified proxy header")
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	req.Header.Set(oauth2ProxyAccessTokenHeader, "attacker-controlled")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("local session behavior changed: %d", w.Code)
	}
}
