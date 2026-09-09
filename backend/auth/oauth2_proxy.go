package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	oauth2ProxyUserHeader     = "X-Auth-Request-User"
	oauth2ProxyUsernameHeader = "X-Auth-Request-Preferred-Username"
	oauth2ProxyEmailHeader    = "X-Auth-Request-Email"
	oauth2ProxyGroupsHeader   = "X-Auth-Request-Groups"
)

// OAuth2ProxyMiddleware accepts a human identity asserted by a trusted
// oauth2-proxy and personal access tokens used by spk-rayjob. Browser bearer
// tokens and platform-local sessions are intentionally not accepted here.
//
// This middleware must only be enabled behind an ingress that runs oauth2
// authentication and overwrites these X-Auth-Request-* headers.
func OAuth2ProxyMiddleware(pat PATVerifier, required bool, groupPrefix string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authorization := c.GetHeader("Authorization"); authorization != "" {
			rawToken, err := ExtractBearer(authorization)
			if err != nil || !strings.HasPrefix(rawToken, "rpt_") {
				abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
				return
			}
			authenticatePAT(c, pat, rawToken)
			return
		}

		principal, err := OAuth2ProxyPrincipal(c.Request.Header, groupPrefix)
		if err != nil {
			if !required {
				c.Next()
				return
			}
			abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "oauth2 proxy authentication is required")
			return
		}
		setPrincipal(c, principal)
		c.Next()
	}
}

// OAuth2ProxyPrincipal converts the standard auth_request response headers
// into the platform's immutable principal. Groups encode both the tenant and
// roles, matching the existing OIDC authorization model.
func OAuth2ProxyPrincipal(headers http.Header, groupPrefix string) (Principal, error) {
	subject := strings.TrimSpace(headers.Get(oauth2ProxyUserHeader))
	if subject == "" {
		return Principal{}, fmt.Errorf("%s is required", oauth2ProxyUserHeader)
	}
	groups := oauth2ProxyGroups(headers.Get(oauth2ProxyGroupsHeader))
	if len(groups) == 0 {
		return Principal{}, fmt.Errorf("%s is required", oauth2ProxyGroupsHeader)
	}
	claims := TokenClaims{
		Subject:           subject,
		PreferredUsername: strings.TrimSpace(headers.Get(oauth2ProxyUsernameHeader)),
		Email:             strings.TrimSpace(headers.Get(oauth2ProxyEmailHeader)),
		Groups:            groups,
		RealmAccess:       RealmAccess{Roles: oauth2ProxyRoles(groups)},
	}
	if claims.PreferredUsername == "" {
		claims.PreferredUsername = subject
	}
	principal, err := claims.Principal(groupPrefix)
	if err != nil {
		return Principal{}, err
	}
	if len(principal.Roles) == 0 {
		return Principal{}, fmt.Errorf("oauth2 proxy groups do not contain a platform role")
	}
	principal.AuthType = AuthTypeOAuth2Proxy
	return principal, nil
}

func oauth2ProxyGroups(raw string) []string {
	values := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	groups := make([]string, 0, len(values))
	for _, value := range values {
		group := strings.TrimSpace(value)
		if group == "" {
			continue
		}
		// Keycloak commonly renders groups with a leading slash; the platform's
		// configured prefix is deliberately slashless.
		groups = append(groups, strings.TrimLeft(group, "/"))
	}
	return groups
}

func oauth2ProxyRoles(groups []string) []string {
	roles := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, group := range groups {
		candidate := strings.TrimSpace(group)
		candidate = strings.TrimPrefix(candidate, "role:")
		candidate = strings.TrimPrefix(candidate, "roles/")
		if index := strings.LastIndex(candidate, "/"); index >= 0 {
			candidate = candidate[index+1:]
		}
		for _, role := range []string{"Engineer", "TenantAdmin", "SuperAdmin"} {
			if strings.EqualFold(candidate, role) {
				if _, ok := seen[role]; !ok {
					roles = append(roles, role)
					seen[role] = struct{}{}
				}
			}
		}
	}
	return roles
}
