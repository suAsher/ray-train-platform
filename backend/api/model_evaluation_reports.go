package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	me "ray-train-platform-backend/modelevaluation"
	"ray-train-platform-backend/modellifecycle"
)

func (h *Handler) getModelEvaluationReport(c *gin.Context) {
	e, ok := h.evaluationForMember(c, c.Param("evaluationId"))
	if !ok {
		return
	}
	if e.ReportState != me.ReportValid || e.Report == nil {
		h.modelEvaluationError(c, me.ErrNotReady)
		return
	}
	raw, err := json.Marshal(e.Report)
	if h.modelEvaluationError(c, err) {
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "evaluation-" + e.ID + ".json"}))
	c.Header("X-Content-SHA256", e.ReportSHA256)
	c.Data(200, "application/json; charset=utf-8", raw)
}

// Mount on the unauthenticated internal API group. Authentication here uses
// only the existing 256-bit job token, checked against the exact evaluation job.
func (h *Handler) RegisterModelEvaluationInternalRoutes(group *gin.RouterGroup) {
	limiter := newFixedWindowSourceArtifactLimiter(30, 60, 10000, time.Now)
	internal := group.Group("/jobs/:id/model-evaluation", func(c *gin.Context) {
		if h.modelEvaluations == nil {
			h.modelEvaluationError(c, errors.New("unavailable"))
			c.Abort()
			return
		}
		if !evaluationReservedJobID.MatchString(c.Param("id")) {
			h.modelEvaluationError(c, me.ErrNotFound)
			c.Abort()
			return
		}
		if len(c.GetHeader("Authorization")) > 128 {
			h.writeError(c, 401, "EVALUATION_JOB_TOKEN_REQUIRED", "job-scoped evaluation token is required")
			c.Abort()
			return
		}
		token, ok := trainingEventBearer(c.GetHeader("Authorization"))
		if !ok {
			h.writeError(c, 401, "EVALUATION_JOB_TOKEN_REQUIRED", "job-scoped evaluation token is required")
			c.Abort()
			return
		}
		action := sourceArtifactActionComplete
		if c.Request.Method == http.MethodPost {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(c.Param("id")+"\x00"+c.ClientIP(), action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "evaluation job request rate limit exceeded")
			c.Abort()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		evaluation, err := h.modelEvaluations.AuthorizeEvaluationJobToken(ctx, c.Param("id"), token, time.Now().UTC())
		if h.modelEvaluationError(c, err) {
			c.Abort()
			return
		}
		if evaluation.JobID != c.Param("id") {
			h.modelEvaluationError(c, me.ErrNotFound)
			c.Abort()
			return
		}
		c.Set("model-evaluation-job", evaluation)
		c.Set("model-evaluation-token", token)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	})
	internal.GET("/model", h.downloadEvaluationModel)
	internal.POST("/report", h.storeModelEvaluationReport)
}
func (h *Handler) downloadEvaluationModel(c *gin.Context) {
	e, ok := c.MustGet("model-evaluation-job").(me.Evaluation)
	if !ok || h.models == nil || h.modelSnapshots == nil {
		h.modelEvaluationError(c, me.ErrNotReady)
		return
	}
	if c.GetHeader("Range") != "" {
		h.writeError(c, 416, "EVALUATION_RANGE_UNSUPPORTED", "evaluation model downloads do not support ranges")
		return
	}
	version, err := h.models.GetVersion(c.Request.Context(), e.ModelID, e.VersionID)
	if err != nil || version.ID != e.VersionID || version.ModelID != e.ModelID || version.State != modellifecycle.Ready || version.SHA256 != e.ModelSHA256 || version.FileName != e.FileName || version.SizeBytes < 1 {
		h.modelEvaluationError(c, me.ErrNotReady)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": version.FileName})
	if disposition == "" {
		h.modelEvaluationError(c, me.ErrInvalid)
		return
	}
	controller := http.NewResponseController(c.Writer)
	deadline := time.Now().Add(30 * time.Minute)
	if requestDeadline, ok := c.Request.Context().Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err := controller.SetWriteDeadline(deadline); err == nil {
		defer controller.SetWriteDeadline(time.Time{})
	} else if !errors.Is(err, http.ErrNotSupported) {
		h.modelEvaluationError(c, err)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Length", strconv.FormatInt(version.SizeBytes, 10))
	c.Header("X-Content-SHA256", e.ModelSHA256)
	c.Header("Accept-Ranges", "none")
	if err := h.modelSnapshots.Download(c.Request.Context(), version, c.Writer); err != nil {
		if c.Writer.Written() {
			_ = c.Error(modellifecycle.ErrUnavailable)
			return
		}
		for _, header := range []string{"Content-Length", "Content-Type", "Content-Disposition", "X-Content-SHA256"} {
			c.Header(header, "")
		}
		h.modelEvaluationError(c, err)
	}
}
func (h *Handler) storeModelEvaluationReport(c *gin.Context) {
	token, ok := c.MustGet("model-evaluation-token").([]byte)
	if !ok {
		h.modelEvaluationError(c, me.ErrNotFound)
		return
	}
	deadline := time.Now().Add(2 * time.Minute)
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetReadDeadline(deadline); err == nil {
		defer controller.SetReadDeadline(time.Time{})
	}
	ctx, cancel := context.WithDeadline(c.Request.Context(), deadline)
	defer cancel()
	body := http.MaxBytesReader(c.Writer, c.Request.Body, me.MaxReportBytes)
	defer body.Close()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	raw, err := io.ReadAll(body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			h.writeError(c, 413, "EVALUATION_REPORT_TOO_LARGE", "评估报告不能超过256 KiB")
		} else {
			h.modelEvaluationError(c, me.ErrInvalid)
		}
		return
	}
	e, err := h.modelEvaluations.StoreEvaluationReport(ctx, c.Param("id"), token, raw, time.Now().UTC())
	if h.modelEvaluationError(c, err) {
		return
	}
	h.writeSuccess(c, 200, e)
}
