package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

// getJobSchedulingCapacity is safe for bounded detail-page polling: it shares
// the diagnosis snapshot without querying retained logs or changing job state.
func (h *Handler) getJobSchedulingCapacity(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	capacity := h.jobSchedulingCapacity(c.Request.Context(), *job)
	if capacity == nil {
		capacity = &domain.SchedulingCapacity{
			ObservedAt: time.Now().UTC(), AcceleratorClass: string(job.Spec.AcceleratorClass.Resolved()),
			UnavailableReason: "not_applicable", Fit: domain.SchedulingCapacityFit{Reason: "not_applicable"},
		}
	}
	h.writeSuccess(c, http.StatusOK, schedulingCapacityResponse(*capacity))
}
