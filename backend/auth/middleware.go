package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
)

const principalContextKey = "ray-platform-principal"

type OIDCVerifier interface {
	Verify(context.Context, string) (Principal, error)
}

type PATVerifier interface {
	Authenticate(context.Context, string) (PATIdentity, error)
}

func ExtractBearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", fmt.Errorf("authorization must use Bearer scheme")
	}
	return parts[1], nil
}

func Middleware(validator OIDCVerifier, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken, present, ok := bearerFromRequest(c, required)
		if !ok {
			return
		}
		if !present {
			c.Next()
			return
		}
		if validator == nil {
			abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			return
		}
		principal, err := validator.Verify(c.Request.Context(), rawToken)
		if err != nil {
			abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
			return
		}
		principal.AuthType = AuthTypeOIDC
		principal.Scopes = nil
		setPrincipal(c, principal)
		c.Next()
	}
}

func HybridMiddleware(oidc OIDCVerifier, pat PATVerifier, required bool) gin.HandlerFunc {
	return HybridMiddlewareWithLocal(oidc, pat, nil, required)
}

// HybridMiddlewareWithLocal dispatches on the token prefix: rpt_ is a personal
// access token, rls_ is a local username/password session, anything else is
// treated as an OIDC access token.
func HybridMiddlewareWithLocal(oidc OIDCVerifier, pat PATVerifier, local LocalSessionVerifier, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken, present, ok := bearerFromRequest(c, required)
		if !ok {
			return
		}
		if !present {
			c.Next()
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
		authenticateOIDC(c, oidc, rawToken)
	}
}

func authenticateLocalSession(c *gin.Context, verifier LocalSessionVerifier, rawToken string) {
	if verifier == nil {
		abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
		return
	}
	principal, err := verifier.Authenticate(c.Request.Context(), rawToken)
	if err != nil {
		if errors.Is(err, ErrInvalidLocalSession) {
			abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
			return
		}
		abortAuthentication(c, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "authentication service is unavailable")
		return
	}
	principal.AuthType = AuthTypeLocal
	principal.Scopes = nil
	setPrincipal(c, principal)
	c.Next()
}

func bearerFromRequest(c *gin.Context, required bool) (string, bool, bool) {
	header := c.GetHeader("Authorization")
	if header == "" && !required {
		return "", false, true
	}
	rawToken, err := ExtractBearer(header)
	if err != nil {
		abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return "", false, false
	}
	return rawToken, true, true
}

func authenticateOIDC(c *gin.Context, verifier OIDCVerifier, rawToken string) {
	if verifier == nil {
		abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
		return
	}
	principal, err := verifier.Verify(c.Request.Context(), rawToken)
	if err != nil {
		abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
		return
	}
	principal.AuthType = AuthTypeOIDC
	principal.Scopes = nil
	setPrincipal(c, principal)
	c.Next()
}

func authenticatePAT(c *gin.Context, verifier PATVerifier, rawToken string) {
	if verifier == nil {
		abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
		return
	}
	identity, err := verifier.Authenticate(c.Request.Context(), rawToken)
	if err != nil {
		if errors.Is(err, ErrInvalidPAT) {
			abortAuthentication(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "invalid authentication token")
			return
		}
		abortAuthentication(c, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "authentication service is unavailable")
		return
	}
	principal := clonePrincipal(identity.Principal)
	principal.AuthType = AuthTypePAT
	principal.Scopes = append([]string(nil), identity.Scopes...)
	setPrincipal(c, principal)
	c.Next()
}

func DemoIdentityMiddleware(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if enabled {
			if _, exists := PrincipalFromGin(c); !exists {
				setPrincipal(c, DemoPrincipal())
			}
		}
		c.Next()
	}
}

func RequireScopes(scopes ...string) gin.HandlerFunc {
	required := append([]string(nil), scopes...)
	return func(c *gin.Context) {
		principal, ok := PrincipalFromGin(c)
		if !ok {
			abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			return
		}
		if principal.AuthType == AuthTypePAT {
			for _, scope := range required {
				if !principal.HasScope(scope) {
					abortAuthentication(c, http.StatusForbidden, "INSUFFICIENT_SCOPE", "the token does not have the required scope")
					return
				}
			}
		} else if !isInteractiveAuthType(principal.AuthType) && principal.AuthType != AuthTypeDemo {
			abortAuthentication(c, http.StatusForbidden, "FORBIDDEN", "forbidden")
			return
		}
		c.Next()
	}
}

// isInteractiveAuthType reports whether the principal came from a human login
// (OIDC or a local username/password session) rather than a machine token.
func isInteractiveAuthType(authType AuthenticationType) bool {
	return authType == AuthTypeOIDC || authType == AuthTypeLocal
}

// RequireInteractiveSession guards endpoints that a human must be logged in
// for — workspace and admin actions — while still rejecting machine tokens.
func RequireInteractiveSession(allowDemo bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := PrincipalFromGin(c)
		if !ok {
			abortAuthentication(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			return
		}
		if isInteractiveAuthType(principal.AuthType) || (allowDemo && principal.AuthType == AuthTypeDemo) {
			c.Next()
			return
		}
		abortAuthentication(c, http.StatusForbidden, "INTERACTIVE_LOGIN_REQUIRED", "an interactive user login is required")
	}
}

// OIDCOnly is retained for existing callers and tests; interactive local
// sessions are accepted on the same endpoints.
func OIDCOnly(allowDemo bool) gin.HandlerFunc {
	return RequireInteractiveSession(allowDemo)
}

func PrincipalFromGin(c *gin.Context) (Principal, bool) {
	if c == nil {
		return Principal{}, false
	}
	value, exists := c.Get(principalContextKey)
	principal, ok := value.(Principal)
	return clonePrincipal(principal), exists && ok
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	value := ctx.Value(principalContextKey)
	principal, ok := value.(Principal)
	return clonePrincipal(principal), ok
}

func SetPrincipalContext(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, clonePrincipal(principal))
}

func setPrincipal(c *gin.Context, principal Principal) {
	stored := clonePrincipal(principal)
	c.Set(principalContextKey, stored)
	c.Request = c.Request.WithContext(SetPrincipalContext(c.Request.Context(), stored))
}

func clonePrincipal(principal Principal) Principal {
	principal.Roles = append([]string(nil), principal.Roles...)
	principal.Scopes = append([]string(nil), principal.Scopes...)
	return principal
}

func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := PrincipalFromGin(c)
		if !ok || !principal.Allowed(role) {
			abortAuthentication(c, http.StatusForbidden, "FORBIDDEN", "forbidden")
			return
		}
		c.Next()
	}
}

func abortAuthentication(c *gin.Context, status int, code, message string) {
	requestID := httpapi.RequestID(c.GetHeader("X-Request-ID"))
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.AbortWithStatusJSON(status, httpapi.Failure[any](requestID, code, message))
}
