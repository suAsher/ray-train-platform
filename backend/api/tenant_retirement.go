package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type TenantRetirementStore interface {
	TenantRetirementPreflight(context.Context, string) (repositories.TenantRetirementPreflight, error)
	RetireTenant(context.Context, string, string, func(context.Context, string) ([]string, error)) (repositories.TenantRetirementPreflight, error)
}

func (h *Handler) retirementPrincipal(c *gin.Context) (auth.Principal, bool) {
	p, ok := h.principal(c)
	if !ok {
		h.writeError(c, 401, "AUTH_REQUIRED", "authentication is required")
		return p, false
	}
	if !p.HasRole(domain.RoleSuperAdmin) || !auth.IsInteractiveAuthType(p.AuthType) {
		h.writeError(c, 403, "FORBIDDEN", "interactive super administrator authentication is required")
		return p, false
	}
	return p, true
}

func (h *Handler) retirementInventory(c *gin.Context, p auth.Principal) (repositories.TenantRetirementPreflight, bool) {
	store, ok := h.admin.(TenantRetirementStore)
	if !ok {
		h.writeError(c, 503, "RETIREMENT_UNAVAILABLE", "team retirement is unavailable")
		return repositories.TenantRetirementPreflight{}, false
	}
	result, err := store.TenantRetirementPreflight(c.Request.Context(), c.Param("id"))
	if err != nil {
		status := 500
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = 404
		}
		h.writeError(c, status, "RETIREMENT_PREFLIGHT_FAILED", "could not inspect team resources")
		return result, false
	}
	if p.TenantID == result.TenantID {
		result.Blockers = append(result.Blockers, "current_tenant")
	}
	if result.TenantID == h.bootstrapTenant {
		result.Blockers = append(result.Blockers, "protected_tenant")
	}
	if h.kubernetes == nil {
		result.Blockers = append(result.Blockers, "k8s_state_unknown")
	} else {
		// Namespace is read from the persisted tenant summary, never inferred from input.
		summaries, err := h.admin.ListTenantSummaries(c.Request.Context())
		namespace := ""
		for _, summary := range summaries {
			if summary.ID == result.TenantID {
				namespace = summary.Namespace
			}
		}
		if err != nil || namespace == "" {
			result.Blockers = append(result.Blockers, "k8s_state_unknown")
		} else {
			blockers, err := h.kubernetes.TenantRetirementBlockers(c.Request.Context(), namespace)
			if err != nil {
				result.Blockers = append(result.Blockers, "k8s_state_unknown")
			} else {
				result.Blockers = append(result.Blockers, blockers...)
			}
		}
	}
	result.CanRetire = len(result.Blockers) == 0
	return result, true
}

func (h *Handler) tenantRetirementPreflight(c *gin.Context) {
	p, ok := h.retirementPrincipal(c)
	if !ok {
		return
	}
	result, ok := h.retirementInventory(c, p)
	if ok {
		h.writeSuccess(c, 200, result)
	}
}

func (h *Handler) retireTenant(c *gin.Context) {
	p, ok := h.retirementPrincipal(c)
	if !ok {
		return
	}
	var request struct {
		ConfirmTenantID string `json:"confirmTenantId"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.ConfirmTenantID != c.Param("id") || strings.TrimSpace(request.ConfirmTenantID) == "" {
		h.writeError(c, 400, "TENANT_CONFIRMATION_REQUIRED", "confirmation must match the team ID")
		return
	}
	result, ok := h.retirementInventory(c, p)
	if !ok {
		return
	}
	if result.RetiredAt != nil && result.TenantID != p.TenantID && result.TenantID != h.bootstrapTenant {
		h.writeSuccess(c, http.StatusOK, result)
		return
	}
	if !result.CanRetire {
		c.JSON(http.StatusConflict, gin.H{"success": false, "error": gin.H{"code": "TENANT_RETIREMENT_BLOCKED", "message": "team retirement is blocked"}, "data": result})
		return
	}
	result, err := h.admin.(TenantRetirementStore).RetireTenant(c.Request.Context(), result.TenantID, p.Subject, h.kubernetes.TenantRetirementBlockers)
	if err != nil {
		if errors.Is(err, repositories.ErrTenantRetirementBlocked) {
			c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "TENANT_RETIREMENT_BLOCKED", "message": "team resources changed"}, "data": result})
		} else {
			h.writeError(c, 500, "TENANT_RETIREMENT_FAILED", "could not retire team")
		}
		return
	}
	h.writeSuccess(c, 200, result)
}

type TenantWriteFence interface {
	WithActiveTenantWrite(context.Context, string, func() error) error
}

func (h *Handler) activeProxyTenant(c *gin.Context, tenantID string) bool {
	if store, ok := h.repository.(interface {
		TenantExists(context.Context, string) (bool, error)
	}); ok {
		exists, err := store.TenantExists(c.Request.Context(), tenantID)
		if err != nil || !exists {
			h.writeError(c, http.StatusForbidden, "TENANT_INACTIVE", "team is inactive or unavailable")
			return false
		}
	}
	return true
}

func (h *Handler) TenantWriteGuard() gin.HandlerFunc {
	if store, ok := h.admin.(TenantWriteFence); ok {
		return TenantWriteGuard(store)
	}
	return func(c *gin.Context) { c.Next() }
}

// TenantWriteGuard must follow authentication on all authenticated routers.
// The actor's fence also covers retirement: self-retirement is forbidden, and
// the other team's exclusive fence is nonblocking, so cross-retirement cannot
// deadlock or let an administrator act after their own team is retired.
func TenantWriteGuard(store TenantWriteFence) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromContext(c.Request.Context())
		if !ok {
			c.Next()
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			// Check lifecycle before reads without holding a connection while
			// streaming. Missing identities can still reach initial OIDC /me.
			if err := store.WithActiveTenantWrite(c.Request.Context(), p.TenantID, func() error { return nil }); err != nil {
				c.AbortWithStatusJSON(403, gin.H{"success": false, "error": gin.H{"code": "TENANT_INACTIVE", "message": "team is inactive or unavailable"}})
				return
			}
			c.Next()
			return
		}
		err := store.WithActiveTenantWrite(c.Request.Context(), p.TenantID, func() error { c.Next(); return nil })
		if err != nil {
			c.AbortWithStatusJSON(403, gin.H{"success": false, "error": gin.H{"code": "TENANT_INACTIVE", "message": "team is inactive or unavailable"}})
		}
	}
}
