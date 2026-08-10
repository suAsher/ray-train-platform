package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// RegisterUserAdminRoutes mounts local account management. Creating people is
// an administrative action, so every handler re-checks the role rather than
// relying on where the group happens to be mounted.
func (h *LocalAuthHandler) RegisterUserAdminRoutes(group *gin.RouterGroup) {
	group.GET("/local-users", h.listUsers)
	group.POST("/local-users", h.createUser)
}

type createUserRequest struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
}

func (h *LocalAuthHandler) administrator(c *gin.Context) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFromGin(c)
	if !ok {
		writeAuthError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		writeAuthError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *LocalAuthHandler) createUser(c *gin.Context) {
	principal, ok := h.administrator(c)
	if !ok {
		return
	}
	if h.store == nil {
		writeAuthError(c, http.StatusServiceUnavailable, "LOCAL_LOGIN_DISABLED", "local accounts are not enabled")
		return
	}
	var request createUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	username := domain.NormalizeUsername(request.Username)
	if err := domain.ValidateUsername(username); err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_USERNAME", err.Error())
		return
	}
	roles, err := domain.NormalizeRoles(request.Roles)
	if err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_ROLE", err.Error())
		return
	}
	hash, err := domain.HashPassword(request.Password)
	if err != nil {
		writeAuthError(c, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	id, err := h.newID()
	if err != nil {
		writeAuthError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate user id")
		return
	}

	// The tenant always comes from the caller's own principal. Accepting it
	// from the body would let a tenant admin create accounts elsewhere.
	user := domain.LocalUser{
		ID: id, Username: username, TenantID: principal.TenantID,
		Roles: roles, PasswordHash: hash,
	}
	if err := h.store.EnsureIdentity(c.Request.Context(), auth.Principal{
		Subject: id, Username: username, TenantID: principal.TenantID, Roles: roles, AuthType: auth.AuthTypeLocal,
	}); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "IDENTITY_PERSIST_FAILED", "could not persist identity")
		return
	}
	if err := h.store.CreateLocalUser(c.Request.Context(), user); err != nil {
		if errors.Is(err, repositories.ErrUsernameTaken) {
			writeAuthError(c, http.StatusConflict, "USERNAME_TAKEN", "the username is already in use")
			return
		}
		writeAuthError(c, http.StatusInternalServerError, "USER_CREATE_FAILED", "could not create the account")
		return
	}
	user.PasswordHash = ""
	writeAuthSuccess(c, http.StatusCreated, user)
}

func (h *LocalAuthHandler) listUsers(c *gin.Context) {
	principal, ok := h.administrator(c)
	if !ok {
		return
	}
	if h.store == nil {
		writeAuthError(c, http.StatusServiceUnavailable, "LOCAL_LOGIN_DISABLED", "local accounts are not enabled")
		return
	}
	users, err := h.store.ListLocalUsers(c.Request.Context())
	if err != nil {
		writeAuthError(c, http.StatusInternalServerError, "USER_LIST_FAILED", "could not list accounts")
		return
	}
	visible := make([]domain.LocalUser, 0, len(users))
	for _, user := range users {
		if !principal.HasRole(domain.RoleSuperAdmin) && user.TenantID != principal.TenantID {
			continue
		}
		user.PasswordHash = ""
		visible = append(visible, user)
	}
	writeAuthSuccess(c, http.StatusOK, visible)
}
