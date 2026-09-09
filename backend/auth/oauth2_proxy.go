package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

const oauth2ProxyAccessTokenHeader = "X-Auth-Request-Access-Token"

// OAuth2ProxyMiddleware accepts personal access tokens used by spk-rayjob, a
// human access token forwarded by oauth2-proxy, and (during a staged migration)
// existing local sessions. Raw identity/group headers are never sufficient for
// authorization because the CLI endpoint is reachable by external clients.
func OAuth2ProxyMiddleware(oidc OIDCVerifier, pat PATVerifier, local LocalSessionVerifier, required bool) gin.HandlerFunc {
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
			abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
			return
		}

		rawToken := strings.TrimSpace(c.GetHeader(oauth2ProxyAccessTokenHeader))
		if rawToken == "" || oidc == nil {
			if !required {
				c.Next()
				return
			}
			abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "oauth2 proxy authentication is required")
			return
		}
		principal, err := oidc.Verify(c.Request.Context(), rawToken)
		if err != nil {
			abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid oauth2 proxy access token")
			return
		}
		principal.AuthType = AuthTypeOAuth2Proxy
		principal.Scopes = nil
		setPrincipal(c, principal)
		c.Next()
	}
}
