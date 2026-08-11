package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type GitCredentialStore interface {
	UpsertGitCredential(ctx context.Context, credential domain.GitCredential) error
	ListGitCredentials(ctx context.Context, tenantID string) ([]domain.GitCredential, error)
	DeleteGitCredential(ctx context.Context, tenantID, id string) (string, error)
}

func (h *Handler) RegisterGitCredentialRoutes(group *gin.RouterGroup) {
	group.GET("/git-credentials", h.listGitCredentials)
	group.POST("/git-credentials", h.createGitCredential)
	group.DELETE("/git-credentials/:id", h.deleteGitCredential)
}

type createGitCredentialRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

func (h *Handler) listGitCredentials(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.gitCredentials == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_CREDENTIALS_UNAVAILABLE", "git credentials are not configured")
		return
	}
	credentials, err := h.gitCredentials.ListGitCredentials(c.Request.Context(), principal.TenantID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_LIST_FAILED", "could not list git credentials")
		return
	}
	// The response only ever carries the Secret's name, never the token.
	h.writeSuccess(c, http.StatusOK, credentials)
}

func (h *Handler) createGitCredential(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if h.gitCredentials == nil || h.kubernetes == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_CREDENTIALS_UNAVAILABLE", "git credentials are not configured")
		return
	}
	var request createGitCredentialRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	if request.Token == "" {
		h.writeError(c, http.StatusBadRequest, "TOKEN_REQUIRED", "an access token or password is required")
		return
	}
	id, err := h.newID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate credential id")
		return
	}
	namespace := "tenant-" + sanitizeDNS(principal.TenantID)
	secretName := domain.GitCredentialSecretName(principal.TenantID, request.Host)

	// The token goes straight into a Kubernetes Secret in the tenant namespace;
	// PostgreSQL only ever sees the Secret's name.
	if err := h.kubernetes.EnsureGitCredentialSecret(c.Request.Context(), namespace, secretName, request.Username, request.Token); err != nil {
		h.writeError(c, http.StatusBadGateway, "GIT_SECRET_WRITE_FAILED", "could not store the credential in Kubernetes")
		return
	}
	credential := domain.GitCredential{
		ID: id, TenantID: principal.TenantID, Name: request.Name, Host: request.Host,
		Username: request.Username, SecretName: secretName, CreatedBy: principal.Subject,
	}
	if err := h.gitCredentials.UpsertGitCredential(c.Request.Context(), credential); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_GIT_CREDENTIAL", err.Error())
		return
	}
	h.writeSuccess(c, http.StatusCreated, credential)
}

func (h *Handler) deleteGitCredential(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if h.gitCredentials == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_CREDENTIALS_UNAVAILABLE", "git credentials are not configured")
		return
	}
	secretName, err := h.gitCredentials.DeleteGitCredential(c.Request.Context(), principal.TenantID, c.Param("id"))
	if errors.Is(err, repositories.ErrGitCredentialNotFound) {
		h.writeError(c, http.StatusNotFound, "GIT_CREDENTIAL_NOT_FOUND", "credential was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_DELETE_FAILED", "could not delete credential")
		return
	}
	if h.kubernetes != nil && secretName != "" {
		namespace := "tenant-" + sanitizeDNS(principal.TenantID)
		_ = h.kubernetes.DeleteSecret(c.Request.Context(), namespace, secretName)
	}
	h.writeSuccess(c, http.StatusOK, map[string]bool{"deleted": true})
}
