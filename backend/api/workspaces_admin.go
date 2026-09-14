package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// Administrative management is separate from the owner-only editor and /me
// routes. An administrator may release resources without opening user data.
type administrativeWorkspaceManager interface {
	GetWorkspaceByID(context.Context, string, string) (*domain.DevWorkspace, error)
	UpdateWorkspaceStateByID(context.Context, string, domain.WorkspaceState) error
}

var _ administrativeWorkspaceManager = (*repositories.GormRepository)(nil)

func (h *Handler) adminStopWorkspace(c *gin.Context) {
	principal, ok := h.adminPrincipal(c)
	if !ok {
		return
	}
	tenantID := strings.TrimSpace(principal.TenantID)
	if principal.HasRole(domain.RoleSuperAdmin) {
		tenantID = ""
	} else if tenantID == "" {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "a tenant administrator must belong to a team")
		return
	}
	store, ok := h.workspaces.(administrativeWorkspaceManager)
	if !ok || h.kubernetes == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace administration is not configured")
		return
	}
	workspace, err := store.GetWorkspaceByID(c.Request.Context(), c.Param("id"), tenantID)
	if err != nil || workspace == nil {
		h.writeError(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "debug workspace was not found")
		return
	}
	// Resource identity comes exclusively from the authorized database record.
	// Kubernetes also checks the workspace ID label before removing resources.
	if err := h.kubernetes.DeleteRayCluster(c.Request.Context(), workspace.Namespace, workspace.RayClusterName, workspace.ID); err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_STOP_FAILED", "could not stop debug RayCluster")
		return
	}
	if err := h.kubernetes.DeleteWorkspaceService(c.Request.Context(), workspace.Namespace, workspace.RayClusterName, workspace.ID); err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_STOP_FAILED", "could not remove the debug workspace service")
		return
	}
	// Do not update by owner: a concurrent relaunch may have replaced a stopped
	// record with a different workspace belonging to the same user.
	if err := store.UpdateWorkspaceStateByID(c.Request.Context(), workspace.ID, domain.WorkspaceStopped); err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_STATE_FAILED", "could not persist workspace state")
		return
	}
	h.recordAdministrativeAudit(c, repositories.AdministrativeAuditEvent{
		Action: "workspace.stopped", ResourceID: workspace.ID, TargetTenantID: workspace.TenantID,
	})
	h.writeSuccess(c, http.StatusAccepted, map[string]string{"state": string(domain.WorkspaceStopped)})
}
