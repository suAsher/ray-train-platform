package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/repositories"
)

type AdminStore interface {
	ListTenantSummaries(context.Context) ([]repositories.TenantSummary, error)
	ListUserSummaries(context.Context) ([]repositories.UserSummary, error)
}

func (h *Handler) RegisterAdminRoutes(group *gin.RouterGroup) {
	group.GET("/tenants", h.listTenants)
	group.GET("/users", h.listUsers)
}

func (h *Handler) listTenants(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	if h.admin == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ADMIN_UNAVAILABLE", "admin data is not configured")
		return
	}
	items, err := h.admin.ListTenantSummaries(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "TENANT_LIST_FAILED", "could not list tenants")
		return
	}
	if !principal.HasRole("SuperAdmin") {
		filtered := make([]repositories.TenantSummary, 0, 1)
		for _, item := range items {
			if item.ID == principal.TenantID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	h.writeSuccess(c, http.StatusOK, items)
}

func (h *Handler) listUsers(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	if h.admin == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ADMIN_UNAVAILABLE", "admin data is not configured")
		return
	}
	items, err := h.admin.ListUserSummaries(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "USER_LIST_FAILED", "could not list users")
		return
	}
	if !principal.HasRole("SuperAdmin") {
		filtered := make([]repositories.UserSummary, 0)
		for _, item := range items {
			if item.TenantID == principal.TenantID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	h.writeSuccess(c, http.StatusOK, items)
}

func (h *Handler) adminPrincipal(c *gin.Context) (auth.Principal, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if !principal.Allowed("TenantAdmin") {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return auth.Principal{}, false
	}
	return principal, true
}
