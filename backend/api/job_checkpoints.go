package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type checkpointStore interface {
	ListUsableCheckpoints(context.Context, string, string, string) ([]domain.TrainingCheckpoint, error)
}

func (h *Handler) RegisterCheckpointRoutes(group *gin.RouterGroup) {
	read := group.Group("")
	read.Use(auth.RequireScopes(domain.PATScopeJobsRead))
	read.GET("/jobs/:id/checkpoints", h.listJobCheckpoints)
}

func (h *Handler) listJobCheckpoints(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	store, ok := h.repository.(checkpointStore)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "CHECKPOINTS_UNAVAILABLE", "checkpoint service is not configured")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil || (!principal.HasRole(domain.RoleSuperAdmin) && !principal.HasRole(domain.RoleTenantAdmin) && job.UserID != principal.Subject) {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	items, err := store.ListUsableCheckpoints(c.Request.Context(), job.TenantID, job.UserID, job.ID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "CHECKPOINT_LIST_FAILED", "could not list training checkpoints")
		return
	}
	usable := make([]domain.TrainingCheckpoint, 0, len(items))
	for _, item := range items {
		if item.Complete && item.JobID == job.ID && item.TenantID == job.TenantID && item.UserID == job.UserID {
			usable = append(usable, item)
		}
	}
	h.writeSuccess(c, http.StatusOK, map[string]any{"jobId": job.ID, "items": usable})
}
