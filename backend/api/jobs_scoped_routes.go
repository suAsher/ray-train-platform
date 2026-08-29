package api

import (
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func (h *Handler) registerTrainingRoutes(group *gin.RouterGroup) {
	read := group.Group("")
	read.Use(auth.RequireScopes(domain.PATScopeJobsRead))
	read.GET("/jobs", h.listJobs)
	read.GET("/jobs/:id", h.getJob)
	read.GET("/jobs/:id/runtime", h.getJobRuntime)
	read.GET("/jobs/:id/logs", h.getJobLogs)
	read.GET("/jobs/:id/metrics", h.getJobMetrics)
	read.GET("/jobs/:id/gpu-metrics", h.getJobGPUHistory)
	read.GET("/jobs/:id/training-performance", h.getJobTrainingPerformance)
	read.GET("/jobs/:id/experiment", h.getJobExperiment)
	read.GET("/experiments", h.listExperiments)
	read.POST("/jobs/:id/dashboard-access", h.issueJobDashboardAccess)
	read.GET("/jobs/:id/artifacts", h.listJobArtifacts)
	read.GET("/jobs/:id/artifacts/preview", h.previewJobArtifact)

	write := group.Group("")
	write.Use(auth.RequireScopes(domain.PATScopeJobsWrite))
	write.POST("/jobs", h.submitJob)
	write.POST("/jobs/submit", h.submitJob)
	write.POST("/jobs/:id/cancel", h.cancelJob)
	write.DELETE("/jobs/:id", h.cancelJob)
}
