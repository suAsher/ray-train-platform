package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

type GPUAllocationStore interface {
	ListGPUAllocations(context.Context, string, bool) ([]domain.GPUAllocation, error)
}

func (h *Handler) listGPUAllocations(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	if h.gpuAllocations == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GPU_ALLOCATIONS_UNAVAILABLE", "GPU allocation data is not configured")
		return
	}
	allTenants := principal.HasRole(domain.RoleSuperAdmin)
	tenantID := principal.TenantID
	if allTenants {
		tenantID = ""
	}
	items, err := h.gpuAllocations.ListGPUAllocations(c.Request.Context(), tenantID, allTenants)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "GPU_ALLOCATIONS_LIST_FAILED", "could not list GPU allocations")
		return
	}
	h.writeSuccess(c, http.StatusOK, items)
}
