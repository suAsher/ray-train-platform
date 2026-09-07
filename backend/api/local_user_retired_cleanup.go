package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type retiredLocalUserCleaner interface {
	FindRetiredLocalUserByID(context.Context, string) (domain.LocalUser, error)
	DecommissionRetiredLocalUser(context.Context, string, time.Time) error
}

// This deletion-only path never changes the login lookup or the active-tenant
// fence used by password, role, quota and enable operations. Retired identities
// cannot log in; their already-revoked credentials and historical data stay put.
func (h *LocalAuthHandler) tryDecommissionRetiredUser(c *gin.Context) bool {
	p, ok := auth.PrincipalFromGin(c)
	if !ok || !p.HasRole(domain.RoleSuperAdmin) || (p.AuthType != auth.AuthTypeLocal && p.AuthType != auth.AuthTypeOIDC) {
		return false
	}
	cleaner, ok := h.store.(retiredLocalUserCleaner)
	if !ok {
		return false
	}
	target, err := cleaner.FindRetiredLocalUserByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, repositories.ErrLocalUserNotFound) {
		return false
	}
	if err != nil {
		writeAuthError(c, http.StatusInternalServerError, "LOCAL_USER_LOOKUP_FAILED", "could not read local account")
		return true
	}
	if !canManageUser(p, target) {
		writeAuthError(c, http.StatusForbidden, "USER_NOT_MANAGEABLE", "the selected account cannot be managed by this administrator")
		return true
	}
	if err := cleaner.DecommissionRetiredLocalUser(c.Request.Context(), target.ID, h.now().UTC()); err != nil {
		switch {
		case errors.Is(err, repositories.ErrLocalUserActiveWorkloads):
			writeAuthError(c, http.StatusConflict, "USER_HAS_ACTIVE_WORKLOADS", "the account still owns active workloads")
		case errors.Is(err, repositories.ErrLocalUserNotFound):
			writeAuthError(c, http.StatusNotFound, "LOCAL_USER_NOT_FOUND", "local account was not found")
		default:
			writeAuthError(c, http.StatusInternalServerError, "USER_DECOMMISSION_FAILED", "could not decommission the account")
		}
		return true
	}
	h.auditLocalAccountAction(c, "local_user.decommissioned", target.ID, p)
	writeAuthSuccess(c, http.StatusOK, map[string]bool{"decommissioned": true, "storageRetained": true})
	return true
}
