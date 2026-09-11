package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

type mlflowIntegrationProvider interface {
	QueryJobRun(context.Context, string, string, string, string) (observability.JobExperiment, error)
	LogJobRunBatch(context.Context, string, string, string, string, observability.MLflowLogBatch) error
}

func (h *Handler) registerMLflowIntegrationRoutes(group *gin.RouterGroup) {
	limiter := newFixedWindowSourceArtifactLimiter(60, 120, 10000, time.Now)
	read := group.Group("", auth.RequireScopes(domain.PATScopeJobsRead), h.mlflowIntegrationGuard(limiter, false))
	read.GET("/jobs/:id/mlflow/runs/:runId", h.getMLflowIntegrationRun)
	write := group.Group("", auth.RequireScopes(domain.PATScopeJobsRead, domain.PATScopeMLflowWrite), h.mlflowIntegrationGuard(limiter, true))
	write.POST("/jobs/:id/mlflow/runs/:runId/log-batch", h.logMLflowIntegrationBatch)
}

func (h *Handler) mlflowIntegrationGuard(limiter SourceArtifactLimiter, write bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromGin(c)
		if !ok {
			h.writeError(c, 401, "AUTH_REQUIRED", "authentication is required")
			c.Abort()
			return
		}
		if write && p.AuthType != auth.AuthTypePAT {
			h.writeError(c, 403, "MLFLOW_PAT_REQUIRED", "MLflow writes require a personal access token")
			c.Abort()
			return
		}
		action := sourceArtifactActionComplete
		if write {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(p.TenantID+"\x00"+p.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "MLflow request rate limit exceeded")
			c.Abort()
			return
		}
		if !mlflowDashboardRunIDPattern.MatchString(c.Param("runId")) {
			h.writeError(c, 404, "MLFLOW_RUN_NOT_FOUND", "MLflow run was not found")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func (h *Handler) mlflowIntegrationJob(c *gin.Context, write bool) (auth.Principal, *domain.TrainingJob, mlflowIntegrationProvider, bool) {
	p, _ := auth.PrincipalFromGin(c)
	// Deliberately use current-tenant lookup even for administrators.
	job, err := h.repository.Get(c.Request.Context(), p.TenantID, c.Param("id"))
	if err != nil || job == nil || job.TenantID != p.TenantID {
		h.writeError(c, 404, "JOB_NOT_FOUND", "training job was not found")
		return p, nil, nil, false
	}
	if job.UserID != p.Subject && (write || !p.Allowed(domain.RoleTenantAdmin)) {
		h.writeError(c, 403, "EXPERIMENT_FORBIDDEN", "MLflow access is not allowed for this job")
		return p, nil, nil, false
	}
	provider, ok := h.experiments.(mlflowIntegrationProvider)
	if !ok {
		h.writeError(c, 503, "MLFLOW_UNAVAILABLE", "MLflow integration is not configured")
		return p, nil, nil, false
	}
	return p, job, provider, true
}

func (h *Handler) getMLflowIntegrationRun(c *gin.Context) {
	_, job, provider, ok := h.mlflowIntegrationJob(c, false)
	if !ok {
		return
	}
	result, err := provider.QueryJobRun(c.Request.Context(), job.TenantID, job.ID, job.UserID, c.Param("runId"))
	if err != nil {
		h.mlflowIntegrationError(c, err)
		return
	}
	h.writeSuccess(c, 200, result)
}

func (h *Handler) logMLflowIntegrationBatch(c *gin.Context) {
	p, job, provider, ok := h.mlflowIntegrationJob(c, true)
	if !ok {
		return
	}
	batch, ok := h.decodeMLflowIntegrationBatch(c)
	if !ok {
		return
	}
	if h.mlflowDashboardStore == nil {
		h.writeError(c, 503, "MLFLOW_AUDIT_UNAVAILABLE", "MLflow write audit is unavailable")
		return
	}
	started := time.Now()
	event := repositories.MLflowAuditEvent{Action: repositories.MLflowAuditRunLogBatch, Principal: p, Method: http.MethodPost, Path: c.Request.URL.Path, Status: http.StatusProcessing, RequestID: c.GetHeader("X-Request-ID")}
	if err := h.mlflowDashboardStore.CreateMLflowAuditLog(c.Request.Context(), event); err != nil {
		h.writeError(c, 503, "MLFLOW_AUDIT_UNAVAILABLE", "MLflow write audit is unavailable")
		return
	}
	err := provider.LogJobRunBatch(c.Request.Context(), job.TenantID, job.ID, job.UserID, c.Param("runId"), batch)
	status, code, message := mlflowIntegrationErrorDetails(err)
	completion := event
	completion.Status = status
	completion.Duration = time.Since(started)
	// Preserve outcome auditing even if the client disconnects during MLflow I/O.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	if auditErr := h.mlflowDashboardStore.CreateMLflowAuditLog(auditCtx, completion); auditErr != nil {
		log.Printf("MLflow batch completion audit failed")
		h.writeError(c, 503, "MLFLOW_AUDIT_INCOMPLETE", "MLflow may have accepted the batch; verify the run before retrying")
		return
	}
	if err != nil {
		h.writeError(c, status, code, message)
		return
	}
	h.writeSuccess(c, 200, gin.H{"runId": c.Param("runId"), "logged": true})
}

func (h *Handler) mlflowIntegrationError(c *gin.Context, err error) {
	status, code, message := mlflowIntegrationErrorDetails(err)
	h.writeError(c, status, code, message)
}

func mlflowIntegrationErrorDetails(err error) (int, string, string) {
	switch {
	case err == nil:
		return 200, "", ""
	case errors.Is(err, observability.ErrMLflowRunNotFound):
		return 404, "MLFLOW_RUN_NOT_FOUND", "MLflow run was not found"
	case errors.Is(err, observability.ErrMLflowRunNotRunning):
		return 409, "MLFLOW_RUN_NOT_RUNNING", "only RUNNING MLflow runs accept writes"
	case errors.Is(err, observability.ErrMLflowBatchInvalid):
		return 400, "MLFLOW_BATCH_INVALID", "MLflow batch is invalid"
	case errors.Is(err, observability.ErrMLflowBatchConflict):
		return 409, "MLFLOW_BATCH_CONFLICT", "MLflow rejected the batch; parameters cannot overwrite existing values, and partial writes may have occurred"
	default:
		return 502, "MLFLOW_REQUEST_FAILED", "MLflow request failed; verify the run before retrying writes"
	}
}
