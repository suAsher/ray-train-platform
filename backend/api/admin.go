package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type AdminStore interface {
	ListTenantSummaries(context.Context) ([]repositories.TenantSummary, error)
	ListUserSummaries(context.Context) ([]repositories.UserSummary, error)
	CreateTenant(ctx context.Context, tenant domain.Tenant) error
}

func (h *Handler) RegisterAdminRoutes(group *gin.RouterGroup) {
	group.GET("/tenants", h.listTenants)
	group.POST("/tenants", h.createTenant)
	group.GET("/users", h.listUsers)
}

type createTenantRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	GPUQuota int    `json:"gpuQuota"`
}

// createTenant provisions a team: the database row, its Kubernetes namespace
// and its Kueue LocalQueue, so an administrator does not have to touch kubectl.
func (h *Handler) createTenant(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	// Creating tenants is cluster-wide, so it is reserved for super admins.
	if !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "super administrator role is required")
		return
	}
	if h.admin == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ADMIN_UNAVAILABLE", "admin data is not configured")
		return
	}
	var request createTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	tenantID := sanitizeDNS(request.ID)
	if tenantID == "" || tenantID != strings.ToLower(strings.TrimSpace(request.ID)) {
		h.writeError(c, http.StatusBadRequest, "INVALID_TENANT_ID", "tenant id must be lowercase letters, digits or '-'")
		return
	}
	namespace := "tenant-" + tenantID
	tenant := domain.Tenant{
		ID: tenantID, Name: strings.TrimSpace(request.Name), Namespace: namespace,
		LocalQueue: tenantQueue(tenantID), GPUQuotaLimit: request.GPUQuota,
	}
	if tenant.Name == "" {
		tenant.Name = tenantID
	}
	if err := h.admin.CreateTenant(c.Request.Context(), tenant); err != nil {
		h.writeError(c, http.StatusConflict, "TENANT_CREATE_FAILED", err.Error())
		return
	}
	// Provision the cluster side too; without a namespace and LocalQueue the
	// tenant cannot run anything.
	if h.kubernetes != nil {
		if err := h.kubernetes.EnsureNamespace(c.Request.Context(), namespace, tenantID); err != nil {
			h.writeError(c, http.StatusBadGateway, "NAMESPACE_CREATE_FAILED", "tenant saved but its namespace could not be created")
			return
		}
		if err := h.kubernetes.EnsureLocalQueue(c.Request.Context(), namespace, tenant.LocalQueue, h.clusterQueue); err != nil {
			h.writeError(c, http.StatusBadGateway, "QUEUE_PROVISION_FAILED", "tenant saved but its queue could not be created")
			return
		}
	}
	h.writeSuccess(c, http.StatusCreated, tenant)
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
