package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

const oauth2ProxyAccessTokenHeader = "X-Auth-Request-Access-Token"

type OAuth2ProxyAccountResolver interface {
	ResolveOAuth2ProxyAccount(context.Context, string) (domain.LocalUser, bool, error)
}

// OAuth2ProxyMiddleware accepts personal access tokens used by spk-rayjob, a
// human access token forwarded by oauth2-proxy, and (during a staged migration)
// existing local sessions. A verified legacy browser bearer is also accepted
// while the Portal rolls out cookie-only requests. Raw identity/group headers
// are never sufficient because the CLI endpoint is reachable externally.
func OAuth2ProxyMiddleware(oidc OIDCIdentityVerifier, accounts OAuth2ProxyAccountResolver, pat PATVerifier, local LocalSessionVerifier, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authorization := c.GetHeader("Authorization"); authorization != "" {
			rawToken, err := ExtractBearer(authorization)
			if err != nil {
				abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
				return
			}
			if strings.HasPrefix(rawToken, "rpt_") {
				authenticatePAT(c, pat, rawToken)
				return
			}
			if domain.IsLocalSessionToken(rawToken) {
				authenticateLocalSession(c, local, rawToken)
				return
			}
			authenticateOAuth2ProxyAccount(c, oidc, accounts, rawToken)
			return
		}

		rawToken := strings.TrimSpace(c.GetHeader(oauth2ProxyAccessTokenHeader))
		if rawToken == "" {
			if !required {
				c.Next()
				return
			}
			abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "oauth2 proxy authentication is required")
			return
		}
		authenticateOAuth2ProxyAccount(c, oidc, accounts, rawToken)
	}
}

func authenticateOAuth2ProxyAccount(c *gin.Context, oidc OIDCIdentityVerifier, accounts OAuth2ProxyAccountResolver, rawToken string) {
	if oidc == nil || accounts == nil {
		abortAuthentication(c, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "authentication service is unavailable")
		return
	}
	identity, err := oidc.VerifyIdentity(c.Request.Context(), rawToken)
	if err != nil {
		abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid oauth2 proxy access token")
		return
	}
	account, found, err := accounts.ResolveOAuth2ProxyAccount(c.Request.Context(), identity.Username)
	if err != nil {
		abortAuthentication(c, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "authentication service is unavailable")
		return
	}
	if !found || account.Disabled || strings.TrimSpace(account.ID) == "" || strings.TrimSpace(account.TenantID) == "" || len(account.Roles) == 0 {
		abortAuthentication(c, http.StatusForbidden, "ACCOUNT_NOT_PROVISIONED", "account is not provisioned for RayTrain")
		return
	}
	email := strings.TrimSpace(account.Email)
	if email == "" {
		email = identity.Email
	}
	principal := Principal{
		Subject: account.ID, Username: account.Username, Email: email,
		TenantID: account.TenantID, Roles: append([]string(nil), account.Roles...),
		AuthType: AuthTypeOAuth2Proxy,
	}
	setPrincipal(c, principal)
	c.Next()
}
