package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

type TrainingPerformanceProvider interface {
	QueryTrainingPerformance(context.Context, domain.TrainingWorkloadRef, string) (domain.TrainingPerformance, error)
}

var allowedTrainingPerformanceWindows = map[string]struct{}{"15m": {}, "1h": {}, "6h": {}, "24h": {}}

func (h *Handler) getJobTrainingPerformance(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	for parameter := range c.Request.URL.Query() {
		if parameter != "window" {
			h.writeError(c, http.StatusBadRequest, "TRAINING_PERFORMANCE_SELECTOR_FORBIDDEN", "workload selectors are derived from the persisted job")
			return
		}
	}
	window := strings.TrimSpace(c.Query("window"))
	if window == "" {
		window = "1h"
	}
	if _, ok := allowedTrainingPerformanceWindows[window]; !ok {
		h.writeError(c, http.StatusBadRequest, "TRAINING_PERFORMANCE_WINDOW_INVALID", "window must be one of 15m, 1h, 6h, or 24h")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil || !canReadJobGPUHistory(principal, job) {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	provider, ok := h.metrics.(TrainingPerformanceProvider)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "TRAINING_PERFORMANCE_UNAVAILABLE", "training performance metrics are not configured")
		return
	}
	performance, err := provider.QueryTrainingPerformance(c.Request.Context(), domain.TrainingWorkloadRef{
		Namespace: job.KubernetesNS, RayClusterName: job.RayClusterName, RayJobName: job.RayJobName,
	}, window)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "TRAINING_PERFORMANCE_QUERY_FAILED", "could not query training performance")
		return
	}
	performance.Recovery = trainingRecoveryTimeline(c.Request.Context(), h.repository, job)
	h.writeSuccess(c, http.StatusOK, performance)
}

func trainingRecoveryTimeline(ctx context.Context, repository JobRepository, job *domain.TrainingJob) []domain.TrainingRecoveryPoint {
	point := domain.TrainingRecoveryPoint{At: job.UpdatedAt, ClusterAttempt: job.ClusterAttempt, RestartCount: job.WorkerRestartCount, ResumeCheckpointID: job.ResumeCheckpointID}
	if store, ok := repository.(checkpointStore); ok {
		if checkpoints, err := store.ListUsableCheckpoints(ctx, job.TenantID, job.UserID, job.ID); err == nil {
			for _, checkpoint := range checkpoints {
				if checkpoint.ID == job.ResumeCheckpointID && checkpoint.Complete && checkpoint.TenantID == job.TenantID && checkpoint.UserID == job.UserID && checkpoint.JobID == job.ID {
					step := checkpoint.Step
					point.CheckpointStep = &step
					break
				}
			}
		}
	}
	return []domain.TrainingRecoveryPoint{point}
}
