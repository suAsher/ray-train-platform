package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/integrations"
	"strings"
	"time"
)

type MLflowIntegrationOptions struct {
	Pepper []byte
	Now    func() time.Time
	NewID  func() (string, error)
}
type MLflowIntegrationHandler struct {
	store   integrations.ManagementStore
	pepper  []byte
	now     func() time.Time
	newID   func() (string, error)
	limiter SourceArtifactLimiter
}

func NewMLflowIntegrationHandler(store integrations.ManagementStore, options MLflowIntegrationOptions) (*MLflowIntegrationHandler, error) {
	if store == nil || len(options.Pepper) < 32 {
		return nil, fmt.Errorf("integration store and PAT pepper are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = newMLflowIntegrationID
	}
	return &MLflowIntegrationHandler{store: store, pepper: append([]byte(nil), options.Pepper...), now: options.Now, newID: options.NewID, limiter: newFixedWindowSourceArtifactLimiter(20, 60, 10000, options.Now)}, nil
}
func newMLflowIntegrationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
func (h *MLflowIntegrationHandler) RegisterRoutes(group *gin.RouterGroup) {
	g := group.Group("/mlflow/integrations", h.guard)
	g.GET("", h.list)
	g.POST("", h.create)
	g.DELETE("/:integrationId", h.revoke)
	g.GET("/:integrationId/tokens", h.listTokens)
	g.POST("/:integrationId/tokens", h.createToken)
	g.DELETE("/:integrationId/tokens/:tokenId", h.revokeToken)
	g.GET("/:integrationId/grants", h.listGrants)
	g.POST("/:integrationId/grants", h.putGrant)
	g.DELETE("/:integrationId/grants/:experimentId", h.revokeGrant)
}
func (h *MLflowIntegrationHandler) guard(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	p, ok := auth.PrincipalFromGin(c)
	if !ok {
		h.fail(c, 401, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !auth.IsInteractiveAuthType(p.AuthType) || p.IntegrationID != "" || p.Subject == "" || p.TenantID == "" {
		h.fail(c, 403, "INTERACTIVE_LOGIN_REQUIRED", "an interactive user login is required")
		return
	}
	action := sourceArtifactActionCreate
	if c.Request.Method == http.MethodGet {
		action = sourceArtifactActionComplete
	}
	if allowed, _ := h.limiter.Allow(p.TenantID+"\x00"+p.Subject, action); !allowed {
		c.Header("Retry-After", "60")
		h.fail(c, 429, "RATE_LIMITED", "integration management request rate limit exceeded")
		return
	}
	for _, key := range []string{"integrationId", "tokenId", "experimentId"} {
		if value := c.Param(key); value != "" && !integrations.ValidID(value) {
			h.fail(c, 404, "INTEGRATION_NOT_FOUND", "integration resource was not found")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}
func (h *MLflowIntegrationHandler) bind(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.fail(c, 400, "INVALID_JSON", "request body is invalid or exceeds 16 KiB")
		return false
	}
	if err := checkIntegrationManagementJSON(json.NewDecoder(bytes.NewReader(body)), 0); err != nil {
		h.fail(c, 400, "INVALID_JSON", "request body contains invalid or duplicate fields")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		h.fail(c, 400, "INVALID_JSON", "request body is invalid or exceeds 16 KiB")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		h.fail(c, 400, "INVALID_JSON", "request body must contain exactly one JSON object")
		return false
	}
	return true
}
func (h *MLflowIntegrationHandler) fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")), code, message))
}
func (h *MLflowIntegrationHandler) ok(c *gin.Context, status int, data any) {
	c.JSON(status, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), data))
}
func (h *MLflowIntegrationHandler) storeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, integrations.ErrInvalid):
		h.fail(c, 400, "INVALID_INTEGRATION_REQUEST", "integration request is invalid")
	case errors.Is(err, integrations.ErrNotFound):
		h.fail(c, 404, "INTEGRATION_NOT_FOUND", "integration resource was not found")
	case errors.Is(err, integrations.ErrLimit):
		h.fail(c, 409, "INTEGRATION_LIMIT_REACHED", "integration resource limit reached")
	case errors.Is(err, integrations.ErrConflict):
		h.fail(c, 409, "INTEGRATION_CONFLICT", "integration resource conflicts with this request")
	default:
		h.fail(c, 503, "INTEGRATION_UNAVAILABLE", "integration management is temporarily unavailable; verify state before retrying")
	}
}
func integrationHTTPPrincipal(c *gin.Context) auth.Principal {
	p, _ := auth.PrincipalFromGin(c)
	return p
}
func (h *MLflowIntegrationHandler) list(c *gin.Context) {
	items, err := h.store.List(c.Request.Context(), integrationHTTPPrincipal(c))
	if err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, gin.H{"items": items})
}
func (h *MLflowIntegrationHandler) create(c *gin.Context) {
	var request struct {
		Name                   string `json:"name"`
		AllowCreateExperiments bool   `json:"allowCreateExperiments"`
	}
	if !h.bind(c, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if !integrations.ValidName(name) {
		h.storeError(c, integrations.ErrInvalid)
		return
	}
	id, err := h.newID()
	if err != nil {
		h.storeError(c, err)
		return
	}
	p := integrationHTTPPrincipal(c)
	identity := integrations.Identity{ID: id, TenantID: p.TenantID, OwnerUserID: p.Subject, Name: name, AllowCreateExperiments: request.AllowCreateExperiments, CreatedAt: h.now().UTC()}
	if err := h.store.Create(c.Request.Context(), p, identity); err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 201, identity)
}
func (h *MLflowIntegrationHandler) revoke(c *gin.Context) {
	if err := h.store.Revoke(c.Request.Context(), integrationHTTPPrincipal(c), c.Param("integrationId"), h.now()); err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, gin.H{"revoked": true})
}
func (h *MLflowIntegrationHandler) listTokens(c *gin.Context) {
	items, err := h.store.ListTokens(c.Request.Context(), integrationHTTPPrincipal(c), c.Param("integrationId"))
	if err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, gin.H{"items": items})
}
func (h *MLflowIntegrationHandler) createToken(c *gin.Context) {
	var request struct {
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expiresInDays"`
	}
	if !h.bind(c, &request) {
		return
	}
	scopes, err := integrations.NormalizeScopes(request.Scopes)
	if err != nil || request.ExpiresInDays < 1 || request.ExpiresInDays > 30 {
		h.storeError(c, integrations.ErrInvalid)
		return
	}
	id, err := h.newID()
	if err != nil {
		h.storeError(c, err)
		return
	}
	p := integrationHTTPPrincipal(c)
	now := h.now().UTC()
	issued, err := domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{ID: id, TenantID: p.TenantID, UserID: "integration:" + c.Param("integrationId"), Scopes: scopes, ExpiresAt: now.Add(time.Duration(request.ExpiresInDays) * 24 * time.Hour)}, h.pepper, now)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if err := h.store.CreateToken(c.Request.Context(), p, c.Param("integrationId"), issued.PersonalAccessToken, issued.Digest); err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 201, issued)
}
func (h *MLflowIntegrationHandler) revokeToken(c *gin.Context) {
	if err := h.store.RevokeToken(c.Request.Context(), integrationHTTPPrincipal(c), c.Param("integrationId"), c.Param("tokenId"), h.now()); err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, gin.H{"revoked": true})
}
func (h *MLflowIntegrationHandler) listGrants(c *gin.Context) {
	items, err := h.store.ListGrants(c.Request.Context(), integrationHTTPPrincipal(c), c.Param("integrationId"))
	if err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, gin.H{"items": items})
}
func (h *MLflowIntegrationHandler) putGrant(c *gin.Context) {
	var request struct {
		ExperimentID string   `json:"experimentId"`
		Permissions  []string `json:"permissions"`
	}
	if !h.bind(c, &request) {
		return
	}
	permissions, err := integrations.NormalizePermissions(request.Permissions)
	if err != nil || !integrations.ValidID(request.ExperimentID) {
		h.storeError(c, integrations.ErrInvalid)
		return
	}
	grant := integrations.Grant{IntegrationID: c.Param("integrationId"), ExperimentID: request.ExperimentID, Permissions: permissions, CreatedAt: h.now().UTC()}
	if err := h.store.PutGrant(c.Request.Context(), integrationHTTPPrincipal(c), grant.IntegrationID, grant); err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, grant)
}
func (h *MLflowIntegrationHandler) revokeGrant(c *gin.Context) {
	if err := h.store.RevokeGrant(c.Request.Context(), integrationHTTPPrincipal(c), c.Param("integrationId"), c.Param("experimentId"), h.now()); err != nil {
		h.storeError(c, err)
		return
	}
	h.ok(c, 200, gin.H{"revoked": true})
}
