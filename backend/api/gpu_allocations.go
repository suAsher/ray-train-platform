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
	// Keep existing callers team-scoped; the occupancy page explicitly requests
	// the global read-only projection. This does not grant job/workspace access.
	allTenants := principal.HasRole(domain.RoleSuperAdmin)
	if scopes, exists := c.Request.URL.Query()["scope"]; exists {
		if len(scopes) != 1 || (scopes[0] != "all" && scopes[0] != "team") {
			h.writeError(c, http.StatusBadRequest, "INVALID_GPU_ALLOCATION_SCOPE", "scope must be all or team")
			return
		}
		allTenants = scopes[0] == "all"
	}
	if h.gpuAllocations == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GPU_ALLOCATIONS_UNAVAILABLE", "GPU allocation data is not configured")
		return
	}
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
