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
	ListGitCredentials(ctx context.Context, tenantID, userID string, scope domain.GitCredentialScope) ([]domain.GitCredential, error)
	GetGitCredential(ctx context.Context, tenantID, id string) (domain.GitCredential, error)
	DeleteGitCredential(ctx context.Context, tenantID, id string) (domain.GitCredential, error)
}

func (h *Handler) RegisterGitCredentialRoutes(group *gin.RouterGroup) {
	group.GET("/git-credentials", h.listGitCredentials)
	group.POST("/git-credentials", h.createGitCredential)
	group.POST("/git-credentials/:id/test", h.testGitCredential)
	group.DELETE("/git-credentials/:id", h.deleteGitCredential)
	// Branch resolution lives with the credentials it may need. A submission
	// still stores a commit; this only removes the manual SHA copy step.
	group.POST("/git/resolve-ref", h.resolveGitRef)
}

type createGitCredentialRequest struct {
	Name     string                    `json:"name"`
	Host     string                    `json:"host"`
	Username string                    `json:"username"`
	Token    string                    `json:"token"`
	Scope    domain.GitCredentialScope `json:"scope"`
}

type testGitCredentialRequest struct {
	RepositoryURL string `json:"repositoryUrl"`
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
	personal, err := h.gitCredentials.ListGitCredentials(c.Request.Context(), principal.TenantID, principal.Subject, domain.GitCredentialScopePersonal)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_LIST_FAILED", "could not list git credentials")
		return
	}
	credentials := personal
	if principal.Allowed(domain.RoleTenantAdmin) {
		team, listErr := h.gitCredentials.ListGitCredentials(c.Request.Context(), principal.TenantID, "", domain.GitCredentialScopeTeam)
		if listErr != nil {
			h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_LIST_FAILED", "could not list team credentials")
			return
		}
		credentials = append(credentials, team...)
	}
	// SecretName and the token are intentionally absent from the JSON model.
	h.writeSuccess(c, http.StatusOK, credentials)
}

func (h *Handler) createGitCredential(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleEngineer) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
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
	if !matchesGitAllowlist("https://"+domain.NormalizeGitHost(request.Host)+"/", h.gitAllowlist) {
		h.writeError(c, http.StatusBadRequest, "GIT_HOST_NOT_ALLOWED", "the Git host is not in the platform allowlist; ask an administrator to approve it before saving a credential")
		return
	}
	scope := request.Scope
	if scope == "" {
		scope = domain.GitCredentialScopePersonal
	}
	if scope != domain.GitCredentialScopePersonal && scope != domain.GitCredentialScopeTeam {
		h.writeError(c, http.StatusBadRequest, "INVALID_GIT_CREDENTIAL_SCOPE", "credential scope must be personal or team")
		return
	}
	if scope == domain.GitCredentialScopeTeam && !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required for a team credential")
		return
	}
	id, err := h.newID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate credential id")
		return
	}
	namespace := "tenant-" + sanitizeDNS(principal.TenantID)
	ownerUserID := ""
	if scope == domain.GitCredentialScopePersonal {
		ownerUserID = principal.Subject
	}
	secretName := domain.GitCredentialSecretName(principal.TenantID, scope, ownerUserID, request.Host)
	credential := domain.GitCredential{
		ID: id, TenantID: principal.TenantID, Scope: scope, OwnerUserID: ownerUserID, Name: request.Name, Host: request.Host,
		Username: request.Username, SecretName: secretName, CreatedBy: principal.Subject,
	}
	if err := credential.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_GIT_CREDENTIAL", err.Error())
		return
	}
	// The token goes straight into a Kubernetes Secret in the tenant namespace;
	// PostgreSQL only ever sees the Secret's name.
	if err := h.kubernetes.EnsureGitCredentialSecret(c.Request.Context(), namespace, secretName, request.Username, request.Token); err != nil {
		h.writeError(c, http.StatusBadGateway, "GIT_SECRET_WRITE_FAILED", "could not store the credential in Kubernetes")
		return
	}
	if err := h.gitCredentials.UpsertGitCredential(c.Request.Context(), credential); err != nil {
		// Do not delete the Secret here. For an existing host/scope the Secret
		// name is stable and may still back the previous valid database row;
		// removing it on a transient database failure would break running and
		// future private-repository jobs. A newly written orphan is safer than
		// destroying an existing credential and remains platform-managed.
		h.writeError(c, http.StatusBadRequest, "INVALID_GIT_CREDENTIAL", err.Error())
		return
	}
	h.writeSuccess(c, http.StatusCreated, credential)
}

func (h *Handler) testGitCredential(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.gitCredentials == nil || h.kubernetes == nil || h.gitCredentialTester == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_CREDENTIAL_TEST_UNAVAILABLE", "Git credential testing is not configured")
		return
	}
	credential, err := h.gitCredentials.GetGitCredential(c.Request.Context(), principal.TenantID, c.Param("id"))
	if errors.Is(err, repositories.ErrGitCredentialNotFound) {
		h.writeError(c, http.StatusNotFound, "GIT_CREDENTIAL_NOT_FOUND", "Git credential was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_LOOKUP_FAILED", "could not read the Git credential")
		return
	}
	if credential.Scope == domain.GitCredentialScopePersonal && credential.OwnerUserID != principal.Subject && !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "a personal credential can only be tested by its owner")
		return
	}
	if credential.Scope == domain.GitCredentialScopeTeam && !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required to test a team credential")
		return
	}
	var request testGitCredentialRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	repositoryURL, err := validateApprovedGitRepositoryURL(request.RepositoryURL, credential.Host, h.gitAllowlist)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_GIT_TEST_REPOSITORY", err.Error())
		return
	}
	namespace := "tenant-" + sanitizeDNS(principal.TenantID)
	username, token, err := h.kubernetes.ReadGitCredentialSecret(c.Request.Context(), namespace, credential.SecretName)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_CREDENTIAL_SECRET_UNAVAILABLE", "could not read the saved Git credential for testing")
		return
	}
	result, err := h.gitCredentialTester.Probe(c.Request.Context(), repositoryURL, username, token)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "GIT_CREDENTIAL_TEST_FAILED", "could not test the Git repository connection")
		return
	}
	h.writeSuccess(c, http.StatusOK, result)
}

func (h *Handler) deleteGitCredential(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.gitCredentials == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_CREDENTIALS_UNAVAILABLE", "git credentials are not configured")
		return
	}
	credential, err := h.gitCredentials.GetGitCredential(c.Request.Context(), principal.TenantID, c.Param("id"))
	if errors.Is(err, repositories.ErrGitCredentialNotFound) {
		h.writeError(c, http.StatusNotFound, "GIT_CREDENTIAL_NOT_FOUND", "credential was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_DELETE_FAILED", "could not delete credential")
		return
	}
	if credential.Scope == domain.GitCredentialScopePersonal && credential.OwnerUserID != principal.Subject && !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "a personal credential can only be removed by its owner")
		return
	}
	if credential.Scope == domain.GitCredentialScopeTeam && !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required to remove a team credential")
		return
	}
	credential, err = h.gitCredentials.DeleteGitCredential(c.Request.Context(), principal.TenantID, credential.ID)
	if errors.Is(err, repositories.ErrGitCredentialNotFound) {
		h.writeError(c, http.StatusNotFound, "GIT_CREDENTIAL_NOT_FOUND", "credential was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GIT_CREDENTIAL_DELETE_FAILED", "could not delete credential")
		return
	}
	if h.kubernetes != nil && credential.SecretName != "" {
		namespace := "tenant-" + sanitizeDNS(principal.TenantID)
		_ = h.kubernetes.DeleteSecret(c.Request.Context(), namespace, credential.SecretName)
	}
	h.writeSuccess(c, http.StatusOK, map[string]bool{"deleted": true})
}
