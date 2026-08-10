package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/repositories"
)

type PersonalAccessTokenStore interface {
	EnsureIdentity(context.Context, auth.Principal) error
	CreatePersonalAccessToken(context.Context, domain.PersonalAccessToken, string) error
	ListPersonalAccessTokens(context.Context, string, string) ([]domain.PersonalAccessToken, error)
	RevokePersonalAccessToken(context.Context, string, string, string, time.Time) error
}

type PersonalAccessTokenOptions struct {
	Pepper            []byte
	DefaultExpiryDays int
	MaxExpiryDays     int
	AllowDemo         bool
	Now               func() time.Time
	NewID             func() (string, error)
}

type PersonalAccessTokenHandler struct {
	store             PersonalAccessTokenStore
	pepper            []byte
	defaultExpiryDays int
	maxExpiryDays     int
	allowDemo         bool
	now               func() time.Time
	newID             func() (string, error)
}

type createPersonalAccessTokenRequest struct {
	Scopes        []string `json:"scopes"`
	ExpiresInDays *int     `json:"expiresInDays"`
}

func NewPersonalAccessTokenHandler(store PersonalAccessTokenStore, options PersonalAccessTokenOptions) (*PersonalAccessTokenHandler, error) {
	if store == nil {
		return nil, fmt.Errorf("personal access token store is required")
	}
	if len(options.Pepper) < 32 {
		return nil, fmt.Errorf("personal access token pepper must contain at least 32 bytes")
	}
	if options.DefaultExpiryDays < 1 || options.MaxExpiryDays < options.DefaultExpiryDays || options.MaxExpiryDays > 365 {
		return nil, fmt.Errorf("personal access token expiry configuration is invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = newPersonalAccessTokenID
	}
	return &PersonalAccessTokenHandler{
		store: store, pepper: append([]byte(nil), options.Pepper...),
		defaultExpiryDays: options.DefaultExpiryDays, maxExpiryDays: options.MaxExpiryDays,
		allowDemo: options.AllowDemo, now: options.Now, newID: options.NewID,
	}, nil
}

func (h *PersonalAccessTokenHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.POST("/personal-access-tokens", h.create)
	group.GET("/personal-access-tokens", h.list)
	group.DELETE("/personal-access-tokens/:id", h.revoke)
}

func (h *PersonalAccessTokenHandler) create(c *gin.Context) {
	principal, ok := h.managementPrincipal(c)
	if !ok {
		return
	}
	request, ok := h.bindCreateRequest(c)
	if !ok {
		return
	}
	if err := h.store.EnsureIdentity(c.Request.Context(), principal); err != nil {
		h.writeError(c, http.StatusInternalServerError, "IDENTITY_PERSIST_FAILED", "could not persist authenticated identity")
		return
	}
	issued, err := h.issue(principal, request)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "PAT_CREATE_FAILED", "could not create personal access token")
		return
	}
	if err := h.store.CreatePersonalAccessToken(c.Request.Context(), issued.PersonalAccessToken, issued.Digest); err != nil {
		h.writeError(c, http.StatusInternalServerError, "PAT_CREATE_FAILED", "could not create personal access token")
		return
	}
	h.writeSuccess(c, http.StatusCreated, issued)
}

func (h *PersonalAccessTokenHandler) bindCreateRequest(c *gin.Context) (createPersonalAccessTokenRequest, bool) {
	var request createPersonalAccessTokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return createPersonalAccessTokenRequest{}, false
	}
	if request.ExpiresInDays != nil && (*request.ExpiresInDays < 1 || *request.ExpiresInDays > h.maxExpiryDays) {
		h.writeError(c, http.StatusBadRequest, "INVALID_PAT_EXPIRY", "expiresInDays is outside the allowed range")
		return createPersonalAccessTokenRequest{}, false
	}
	if len(request.Scopes) > 0 {
		if _, err := domain.NormalizePATScopes(request.Scopes); err != nil {
			h.writeError(c, http.StatusBadRequest, "INVALID_PAT_SCOPES", "one or more personal access token scopes are invalid")
			return createPersonalAccessTokenRequest{}, false
		}
	}
	return request, true
}

func (h *PersonalAccessTokenHandler) issue(principal auth.Principal, request createPersonalAccessTokenRequest) (domain.IssuedPersonalAccessToken, error) {
	id, err := h.newID()
	if err != nil {
		return domain.IssuedPersonalAccessToken{}, err
	}
	days := h.defaultExpiryDays
	if request.ExpiresInDays != nil {
		days = *request.ExpiresInDays
	}
	scopes := request.Scopes
	if len(scopes) == 0 {
		scopes = []string{domain.PATScopeJobsRead, domain.PATScopeJobsWrite, domain.PATScopeSourcesWrite}
	}
	now := h.now().UTC()
	return domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{
		ID: id, TenantID: principal.TenantID, UserID: principal.Subject,
		Scopes: scopes, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour),
	}, h.pepper, now)
}

func (h *PersonalAccessTokenHandler) list(c *gin.Context) {
	principal, ok := h.managementPrincipal(c)
	if !ok {
		return
	}
	items, err := h.store.ListPersonalAccessTokens(c.Request.Context(), principal.TenantID, principal.Subject)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "PAT_LIST_FAILED", "could not list personal access tokens")
		return
	}
	h.writeSuccess(c, http.StatusOK, items)
}

func (h *PersonalAccessTokenHandler) revoke(c *gin.Context) {
	principal, ok := h.managementPrincipal(c)
	if !ok {
		return
	}
	err := h.store.RevokePersonalAccessToken(c.Request.Context(), principal.TenantID, principal.Subject, c.Param("id"), h.now().UTC())
	if errors.Is(err, repositories.ErrPersonalAccessTokenNotFound) {
		h.writeError(c, http.StatusNotFound, "PAT_NOT_FOUND", "personal access token was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "PAT_REVOKE_FAILED", "could not revoke personal access token")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]bool{"revoked": true})
}

func (h *PersonalAccessTokenHandler) managementPrincipal(c *gin.Context) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFromGin(c)
	if !ok && h.allowDemo {
		principal, ok = auth.DemoPrincipal(), true
	}
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if principal.AuthType != auth.AuthTypeOIDC && !(h.allowDemo && principal.AuthType == auth.AuthTypeDemo) {
		h.writeError(c, http.StatusForbidden, "OIDC_REQUIRED", "OIDC authentication is required")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *PersonalAccessTokenHandler) writeSuccess(c *gin.Context, status int, data any) {
	h.disableCaching(c)
	c.JSON(status, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), data))
}

func (h *PersonalAccessTokenHandler) writeError(c *gin.Context, status int, code, message string) {
	h.disableCaching(c)
	c.JSON(status, httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")), code, message))
}

func (h *PersonalAccessTokenHandler) disableCaching(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}

func newPersonalAccessTokenID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random personal access token id: %w", err)
	}
	return "pat-" + hex.EncodeToString(value), nil
}
