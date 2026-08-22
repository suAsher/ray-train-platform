package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

// RegisterUserAdminRoutes mounts local account management. Creating people is
// an administrative action, so every handler re-checks the role rather than
// relying on where the group happens to be mounted.
func (h *LocalAuthHandler) RegisterUserAdminRoutes(group *gin.RouterGroup) {
	group.GET("/local-users", h.listUsers)
	group.POST("/local-users", h.createUser)
	group.POST("/local-users/:id/storage-quota", h.setPersonalStorageQuota)
	group.POST("/local-users/:id/reset-password", h.resetUserPassword)
	// Roles were only ever set at account creation, so an Engineer could never
	// be promoted to manage the team's shared data.
	group.POST("/local-users/:id/roles", h.setUserRoles)
	group.POST("/local-users/:id/disable", h.disableUser)
	group.POST("/local-users/:id/enable", h.enableUser)
	group.DELETE("/local-users/:id", h.decommissionUser)
	group.POST("/storage-governance/objectset/prepare", h.prepareObjectSetBucket)
}

type createUserRequest struct {
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	Roles           []string `json:"roles"`
	TenantID        string   `json:"tenantId"`
	StorageQuotaGiB int64    `json:"storageQuotaGiB"`
}

type resetUserPasswordRequest struct {
	NewPassword string `json:"newPassword"`
}

type personalStorageQuotaRequest struct {
	StorageQuotaGiB int64 `json:"storageQuotaGiB"`
}

// localUserDecommissioner is intentionally optional so alternate auth stores
// can keep operating while the production repository gains the audited,
// retention-preserving account removal workflow.
type localUserDecommissioner interface {
	DecommissionLocalUser(context.Context, string, time.Time) error
}

type localUserResponse struct {
	domain.LocalUser
	StorageQuota *objectstore.PersonalStorageQuota `json:"storageQuota,omitempty"`
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
	if !canCreateRoles(principal, roles) {
		writeAuthError(c, http.StatusForbidden, "ROLE_NOT_ASSIGNABLE", "the requested role cannot be assigned by this administrator")
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

	tenantID := principal.TenantID
	requestedTenantID := strings.TrimSpace(request.TenantID)
	if requestedTenantID != "" && requestedTenantID != principal.TenantID {
		if !principal.HasRole(domain.RoleSuperAdmin) {
			writeAuthError(c, http.StatusForbidden, "TENANT_NOT_ASSIGNABLE", "tenant administrators may only create users in their own tenant")
			return
		}
		tenantID = requestedTenantID
	}
	if principal.HasRole(domain.RoleSuperAdmin) {
		lookup, supported := h.store.(localTenantLookup)
		if !supported {
			writeAuthError(c, http.StatusServiceUnavailable, "TENANT_LOOKUP_UNAVAILABLE", "tenant lookup is not configured")
			return
		}
		exists, lookupErr := lookup.TenantExists(c.Request.Context(), tenantID)
		if lookupErr != nil {
			writeAuthError(c, http.StatusInternalServerError, "TENANT_LOOKUP_FAILED", "could not validate the selected tenant")
			return
		}
		if !exists {
			writeAuthError(c, http.StatusBadRequest, "TENANT_NOT_FOUND", "select an existing tenant before creating its users")
			return
		}
	}
	user := domain.LocalUser{
		ID: id, Username: username, TenantID: tenantID,
		StorageKey: username, Roles: roles, PasswordHash: hash,
	}
	userPrincipal := auth.Principal{
		Subject: id, Username: username, TenantID: tenantID, Roles: roles, AuthType: auth.AuthTypeLocal,
	}
	if h.personalStorageQuotaEnabled {
		quotaBytes, quotaErr := personalStorageQuotaBytes(request.StorageQuotaGiB, true)
		if quotaErr != nil {
			writeAuthError(c, http.StatusBadRequest, "INVALID_STORAGE_QUOTA", "storage quota must be a positive whole number of GiB")
			return
		}
		if h.personalStorageQuota == nil {
			writeAuthError(c, http.StatusServiceUnavailable, "STORAGE_QUOTA_UNAVAILABLE", "personal storage quota management is not configured")
			return
		}
		if _, quotaErr = h.personalStorageQuota.EnsurePersonalQuota(c.Request.Context(), tenantID, username, quotaBytes); quotaErr != nil {
			h.writePersonalStorageQuotaError(c, quotaErr)
			return
		}
	}
	if h.personalDataInitializer != nil {
		if err := h.personalDataInitializer.EnsurePersonalDataSpace(c.Request.Context(), userPrincipal); err != nil {
			writeAuthError(c, http.StatusServiceUnavailable, "PERSONAL_DATA_SPACE_INITIALIZATION_FAILED", "could not prepare the user's personal data space; correct object storage readiness and try again")
			return
		}
	}
	if err := h.store.EnsureIdentity(c.Request.Context(), userPrincipal); err != nil {
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
	h.auditLocalAccountAction(c, "local_user.created", user.ID, principal)
	user.PasswordHash = ""
	writeAuthSuccess(c, http.StatusCreated, user)
}

// setPersonalStorageQuota changes the native ObjectSet capacity limit. It is
// deliberately an administrator-only action; storage users cannot enlarge
// their own writable space through the Portal API.
func (h *LocalAuthHandler) setPersonalStorageQuota(c *gin.Context) {
	principal, target, ok := h.manageableUser(c)
	if !ok {
		return
	}
	if h.personalStorageQuota == nil || !h.personalStorageQuotaEnabled {
		writeAuthError(c, http.StatusConflict, "STORAGE_QUOTA_DISABLED", "personal storage quota governance is not enabled")
		return
	}
	var request personalStorageQuotaRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	quotaBytes, err := personalStorageQuotaBytes(request.StorageQuotaGiB, false)
	if err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_STORAGE_QUOTA", "storage quota must be a positive whole number of GiB")
		return
	}
	storageKey := strings.TrimSpace(target.StorageKey)
	if storageKey == "" {
		storageKey = target.ID
	}
	quota, err := h.personalStorageQuota.SetPersonalQuota(c.Request.Context(), target.TenantID, storageKey, quotaBytes)
	if err != nil {
		h.writePersonalStorageQuotaError(c, err)
		return
	}
	h.auditLocalAccountAction(c, "local_user.storage_quota_updated", target.ID, principal)
	writeAuthSuccess(c, http.StatusOK, quota)
}

// prepareObjectSetBucket performs the only bucket-wide storage-governance
// action. It is intentionally not delegated to tenant administrators because
// ObjectSet path depth is an immutable bucket-level contract once sets exist.
func (h *LocalAuthHandler) prepareObjectSetBucket(c *gin.Context) {
	principal, ok := h.administrator(c)
	if !ok {
		return
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		writeAuthError(c, http.StatusForbidden, "FORBIDDEN", "super administrator role is required to initialize bucket storage governance")
		return
	}
	if h.personalStorageQuota == nil || !h.personalStorageQuotaEnabled {
		writeAuthError(c, http.StatusConflict, "STORAGE_QUOTA_DISABLED", "personal storage quota governance is not enabled")
		return
	}
	if err := h.personalStorageQuota.PrepareBucket(c.Request.Context()); err != nil {
		h.writePersonalStorageQuotaError(c, err)
		return
	}
	h.auditLocalAccountAction(c, "storage.objectset_prepared", "personal-storage", principal)
	writeAuthSuccess(c, http.StatusOK, map[string]bool{"prepared": true})
}

func personalStorageQuotaBytes(gib int64, allowDefault bool) (int64, error) {
	if gib == 0 && allowDefault {
		return 0, nil
	}
	if gib <= 0 || gib > math.MaxInt64/objectstore.GiB {
		return 0, objectstore.ErrInvalidPersonalStorageQuota
	}
	return gib * objectstore.GiB, nil
}

func (h *LocalAuthHandler) writePersonalStorageQuotaError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, objectstore.ErrInvalidPersonalStorageQuota):
		writeAuthError(c, http.StatusBadRequest, "INVALID_STORAGE_QUOTA", "storage quota is outside the platform's allowed range")
	case errors.Is(err, objectstore.ErrObjectSetNotReady):
		writeAuthError(c, http.StatusConflict, "STORAGE_OBJECTSET_NOT_READY", "personal storage is not initialized; an administrator must initialize TOS ObjectSet governance first")
	default:
		writeAuthError(c, http.StatusServiceUnavailable, "STORAGE_QUOTA_UNAVAILABLE", "could not update the personal storage quota; try again later")
	}
}

func (h *LocalAuthHandler) resetUserPassword(c *gin.Context) {
	principal, target, ok := h.manageableUser(c)
	if !ok {
		return
	}
	var request resetUserPasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	hash, err := domain.HashPassword(request.NewPassword)
	if err != nil {
		writeAuthError(c, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	if err := h.store.SetLocalUserPassword(c.Request.Context(), target.ID, hash); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "PASSWORD_UPDATE_FAILED", "could not reset password")
		return
	}
	if err := h.store.RevokeAllLocalSessions(c.Request.Context(), target.ID, h.now().UTC()); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "could not revoke previous sessions")
		return
	}
	h.auditLocalAccountAction(c, "local_user.password_reset", target.ID, principal)
	writeAuthSuccess(c, http.StatusOK, map[string]bool{"updated": true})
}

type setUserRolesRequest struct {
	Roles []string `json:"roles"`
}

// localUserRoleSetter is an optional store capability so a deployment without
// it degrades to a clear 503 rather than a compile-time coupling.
type localUserRoleSetter interface {
	SetLocalUserRoles(ctx context.Context, userID string, roles []string) error
}

// setUserRoles changes an existing account's role. Granting the tenant
// administrator role is what unlocks writing to the shared team directory, so
// it is reserved for a super administrator: letting a team lead promote peers
// would let any lead mint more leads without oversight.
func (h *LocalAuthHandler) setUserRoles(c *gin.Context) {
	principal, target, ok := h.manageableUser(c)
	if !ok {
		return
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		writeAuthError(c, http.StatusForbidden, "FORBIDDEN", "super administrator role is required to change account roles")
		return
	}
	setter, supported := h.store.(localUserRoleSetter)
	if !supported {
		writeAuthError(c, http.StatusServiceUnavailable, "ROLE_UPDATE_UNAVAILABLE", "role management is not configured")
		return
	}
	var request setUserRolesRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAuthError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	roles, err := domain.NormalizeRoles(request.Roles)
	if err != nil || len(roles) == 0 {
		writeAuthError(c, http.StatusBadRequest, "INVALID_ROLES", "roles must contain at least one supported role")
		return
	}
	// Cluster-wide privilege is not grantable from a tenant-scoped screen.
	if containsRole(roles, domain.RoleSuperAdmin) {
		writeAuthError(c, http.StatusBadRequest, "INVALID_ROLES", "the super administrator role cannot be granted here")
		return
	}
	if err := setter.SetLocalUserRoles(c.Request.Context(), target.ID, roles); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "ROLE_UPDATE_FAILED", "could not update account roles")
		return
	}
	// Roles are re-read from the user row on every request, so the change is
	// already live. Sessions are revoked anyway so a downgraded operator cannot
	// keep acting from an already-open page.
	if err := h.store.RevokeAllLocalSessions(c.Request.Context(), target.ID, h.now().UTC()); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "roles updated but sessions could not be revoked")
		return
	}
	h.auditLocalAccountAction(c, "local_user.roles_changed", target.ID, principal)
	writeAuthSuccess(c, http.StatusOK, map[string]any{"updated": true, "roles": roles})
}

func (h *LocalAuthHandler) disableUser(c *gin.Context) {
	h.setUserDisabled(c, true)
}

func (h *LocalAuthHandler) enableUser(c *gin.Context) {
	h.setUserDisabled(c, false)
}

// decommissionUser is a soft deletion, not a destructive storage operation.
// It disables the account and revokes sessions only after the repository has
// confirmed that no active workspace or training job would be orphaned. The
// personal TOS root and historical job records remain available for the
// platform retention window and an explicit, separately authorised purge.
func (h *LocalAuthHandler) decommissionUser(c *gin.Context) {
	principal, target, ok := h.manageableUser(c)
	if !ok {
		return
	}
	decommissioner, supported := h.store.(localUserDecommissioner)
	if !supported {
		writeAuthError(c, http.StatusServiceUnavailable, "USER_DECOMMISSION_UNAVAILABLE", "account decommissioning is not configured")
		return
	}
	if err := decommissioner.DecommissionLocalUser(c.Request.Context(), target.ID, h.now().UTC()); err != nil {
		if errors.Is(err, repositories.ErrLocalUserActiveWorkloads) {
			writeAuthError(c, http.StatusConflict, "USER_HAS_ACTIVE_WORKLOADS", "stop the user's running training jobs and debug environment before deleting the account")
			return
		}
		if errors.Is(err, repositories.ErrLocalUserNotFound) {
			writeAuthError(c, http.StatusNotFound, "LOCAL_USER_NOT_FOUND", "local account was not found")
			return
		}
		writeAuthError(c, http.StatusInternalServerError, "USER_DECOMMISSION_FAILED", "could not decommission the account")
		return
	}
	if err := h.store.RevokeAllLocalSessions(c.Request.Context(), target.ID, h.now().UTC()); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "account was disabled but previous sessions could not be revoked")
		return
	}
	h.auditLocalAccountAction(c, "local_user.decommissioned", target.ID, principal)
	writeAuthSuccess(c, http.StatusOK, map[string]bool{"decommissioned": true, "storageRetained": true})
}

func (h *LocalAuthHandler) setUserDisabled(c *gin.Context, disabled bool) {
	principal, target, ok := h.manageableUser(c)
	if !ok {
		return
	}
	if err := h.store.SetLocalUserDisabled(c.Request.Context(), target.ID, disabled); err != nil {
		writeAuthError(c, http.StatusInternalServerError, "USER_STATE_UPDATE_FAILED", "could not update account state")
		return
	}
	if disabled {
		if err := h.store.RevokeAllLocalSessions(c.Request.Context(), target.ID, h.now().UTC()); err != nil {
			writeAuthError(c, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "could not revoke previous sessions")
			return
		}
	}
	action := "local_user.enabled"
	if disabled {
		action = "local_user.disabled"
	}
	h.auditLocalAccountAction(c, action, target.ID, principal)
	writeAuthSuccess(c, http.StatusOK, map[string]bool{"disabled": disabled})
}

func (h *LocalAuthHandler) manageableUser(c *gin.Context) (auth.Principal, domain.LocalUser, bool) {
	principal, ok := h.administrator(c)
	if !ok {
		return auth.Principal{}, domain.LocalUser{}, false
	}
	if h.store == nil {
		writeAuthError(c, http.StatusServiceUnavailable, "LOCAL_LOGIN_DISABLED", "local accounts are not enabled")
		return auth.Principal{}, domain.LocalUser{}, false
	}
	target, err := h.store.FindLocalUserByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, repositories.ErrLocalUserNotFound) {
			writeAuthError(c, http.StatusNotFound, "LOCAL_USER_NOT_FOUND", "local account was not found")
			return auth.Principal{}, domain.LocalUser{}, false
		}
		writeAuthError(c, http.StatusInternalServerError, "LOCAL_USER_LOOKUP_FAILED", "could not read local account")
		return auth.Principal{}, domain.LocalUser{}, false
	}
	if !canManageUser(principal, target) {
		writeAuthError(c, http.StatusForbidden, "USER_NOT_MANAGEABLE", "the selected account cannot be managed by this administrator")
		return auth.Principal{}, domain.LocalUser{}, false
	}
	return principal, target, true
}

func canCreateRoles(principal auth.Principal, roles []string) bool {
	if containsRole(roles, domain.RoleSuperAdmin) {
		return false
	}
	if principal.HasRole(domain.RoleSuperAdmin) {
		return true
	}
	return isEngineerOnly(roles)
}

func canManageUser(principal auth.Principal, target domain.LocalUser) bool {
	if target.ID == principal.Subject || containsRole(target.Roles, domain.RoleSuperAdmin) {
		return false
	}
	if principal.HasRole(domain.RoleSuperAdmin) {
		return true
	}
	return target.TenantID == principal.TenantID && isEngineerOnly(target.Roles)
}

func isEngineerOnly(roles []string) bool {
	return len(roles) == 1 && roles[0] == domain.RoleEngineer
}

func containsRole(roles []string, wanted string) bool {
	for _, role := range roles {
		if role == wanted {
			return true
		}
	}
	return false
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
	visible := make([]localUserResponse, 0, len(users))
	for _, user := range users {
		if !principal.HasRole(domain.RoleSuperAdmin) && user.TenantID != principal.TenantID {
			continue
		}
		user.PasswordHash = ""
		item := localUserResponse{LocalUser: user}
		if h.personalStorageQuotaEnabled && h.personalStorageQuota != nil {
			storageKey := strings.TrimSpace(user.StorageKey)
			if storageKey == "" {
				storageKey = user.ID
			}
			if quota, quotaErr := h.personalStorageQuota.GetPersonalQuota(c.Request.Context(), user.TenantID, storageKey); quotaErr == nil {
				item.StorageQuota = &quota
			}
		}
		visible = append(visible, item)
	}
	writeAuthSuccess(c, http.StatusOK, visible)
}
