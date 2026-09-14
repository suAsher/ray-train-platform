package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type workspaceLifecycleStore interface {
	BeginWorkspaceStop(context.Context, string, string) (*domain.DevWorkspace, error)
	WithWorkspaceOperation(context.Context, string, string, func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error) error
	ApplyWorkspaceObservation(context.Context, string, domain.WorkspaceState, domain.WorkspaceState) (bool, error)
}

var _ workspaceLifecycleStore = (*repositories.GormRepository)(nil)

type workspaceOperationError struct {
	status        int
	code, message string
}

// A rolled-back runtime operation can still have created Kubernetes resources.
// Keep its durable identity and quota until an explicit stop finishes cleanup.
func (h *Handler) recoverWorkspaceLaunch(c *gin.Context, store workspaceLifecycleStore, workspace *domain.DevWorkspace, operationErr error) {
	log.Printf("workspace launch operation failed workspace=%q tenant=%q: %v", workspace.ID, workspace.TenantID, operationErr)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	if _, err := store.BeginWorkspaceStop(ctx, workspace.ID, workspace.TenantID); err != nil {
		log.Printf("workspace launch recovery failed workspace=%q tenant=%q: %v", workspace.ID, workspace.TenantID, err)
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_RECOVERY_FAILED", "could not finish launching or record cleanup; ask a platform administrator to check this workspace")
		return
	}
	h.writeError(c, http.StatusInternalServerError, "WORKSPACE_STATE_FAILED", "could not finish launching the workspace; retry stopping it to clean up before launching again")
}

// completeWorkspaceStop is shared by owner and administrator routes after each
// route's authorization. Only the authorized database identity is accepted.
func (h *Handler) completeWorkspaceStop(c *gin.Context, workspace *domain.DevWorkspace) bool {
	store, ok := h.workspaces.(workspaceLifecycleStore)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace lifecycle management is not configured")
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	if _, err := store.BeginWorkspaceStop(ctx, workspace.ID, workspace.TenantID); err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_STATE_FAILED", "could not persist workspace stop intent")
		return false
	}
	var failure *workspaceOperationError
	err := store.WithWorkspaceOperation(ctx, workspace.ID, workspace.TenantID, func(locked *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		if err := h.kubernetes.DeleteRayCluster(ctx, locked.Namespace, locked.RayClusterName, locked.ID); err != nil {
			failure = &workspaceOperationError{http.StatusBadGateway, "WORKSPACE_STOP_FAILED", "could not stop debug RayCluster; retry stopping the workspace"}
			return err
		}
		if err := h.kubernetes.DeleteWorkspaceService(ctx, locked.Namespace, locked.RayClusterName, locked.ID); err != nil {
			failure = &workspaceOperationError{http.StatusBadGateway, "WORKSPACE_STOP_FAILED", "could not remove the debug workspace service; retry stopping the workspace"}
			return err
		}
		return setState(domain.WorkspaceStopped)
	})
	if err != nil {
		if failure != nil {
			h.writeError(c, failure.status, failure.code, failure.message)
		} else {
			h.writeError(c, http.StatusInternalServerError, "WORKSPACE_STATE_FAILED", "could not finish stopping the workspace; retry the stop operation")
		}
		return false
	}
	return true
}
