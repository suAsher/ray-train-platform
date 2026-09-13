package api

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"net/http"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	ms "ray-train-platform-backend/modelserving"
	"strconv"
	"time"
)

// These routes authenticate only the exact reserved serving job token. Session
// cookies and personal tokens cannot select another model or code snapshot.
func (h *Handler) RegisterModelServingInternalRoutes(group *gin.RouterGroup) {
	limiter := newFixedWindowSourceArtifactLimiter(30, 60, 10000, time.Now)
	internal := group.Group("/jobs/:id/model-serving", func(c *gin.Context) {
		if h.modelServing == nil {
			h.servingError(c, ms.ErrUnavailable)
			c.Abort()
			return
		}
		if !evaluationReservedJobID.MatchString(c.Param("id")) {
			h.servingError(c, ms.ErrNotFound)
			c.Abort()
			return
		}
		if len(c.Request.URL.RawQuery) > 2048 {
			h.servingError(c, ms.ErrInvalid)
			c.Abort()
			return
		}
		raw := c.GetHeader("Authorization")
		if len(raw) > 128 {
			h.writeError(c, 401, "SERVING_JOB_TOKEN_REQUIRED", "job-scoped serving token is required")
			c.Abort()
			return
		}
		token, ok := trainingEventBearer(raw)
		if !ok {
			h.writeError(c, 401, "SERVING_JOB_TOKEN_REQUIRED", "job-scoped serving token is required")
			c.Abort()
			return
		}
		if allowed, _ := limiter.Allow(c.Param("id")+"\x00"+c.ClientIP(), sourceArtifactActionComplete); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "serving job request rate limit exceeded")
			c.Abort()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		d, err := h.modelServing.AuthorizeServingJobToken(ctx, c.Param("id"), token, time.Now().UTC())
		if errors.Is(err, ms.ErrUnauthorized) {
			h.writeError(c, 401, "SERVING_JOB_TOKEN_INVALID", "job-scoped serving token is invalid")
			c.Abort()
			return
		}
		if h.servingError(c, err) {
			c.Abort()
			return
		}
		if d.JobID != c.Param("id") {
			h.servingError(c, ms.ErrNotFound)
			c.Abort()
			return
		}
		if (d.State != ms.Creating && d.State != ms.Submitted && d.State != ms.Ready) || !d.ExpiresAt.After(time.Now().UTC()) {
			h.servingError(c, ms.ErrUnauthorized)
			c.Abort()
			return
		}
		bounded, stop := context.WithDeadline(ctx, d.ExpiresAt)
		defer stop()
		c.Request = c.Request.WithContext(bounded)
		c.Set("model-serving-job", d)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	})
	internal.GET("/code", h.downloadServingCode)
	internal.GET("/model", h.downloadServingModel)
}
func (h *Handler) servingDownloadDeadline(c *gin.Context) (func(), bool) {
	if c.GetHeader("Range") != "" {
		h.writeError(c, 416, "SERVING_RANGE_UNSUPPORTED", "serving downloads do not support ranges")
		return nil, false
	}
	controller := http.NewResponseController(c.Writer)
	deadline := time.Now().Add(30 * time.Minute)
	if requestDeadline, ok := c.Request.Context().Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err := controller.SetWriteDeadline(deadline); err == nil {
		return func() { _ = controller.SetWriteDeadline(time.Time{}) }, true
	} else if !errors.Is(err, http.ErrNotSupported) {
		h.servingError(c, ms.ErrUnavailable)
		return nil, false
	}
	return func() {}, true
}
func (h *Handler) servingDownloadError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if c.Writer.Written() {
		_ = c.Error(ms.ErrUnavailable)
		return
	}
	for _, header := range []string{"Content-Length", "Content-Type", "Content-Disposition", "X-Content-SHA256"} {
		c.Header(header, "")
	}
	h.servingError(c, ms.ErrUnavailable)
}
func (h *Handler) downloadServingCode(c *gin.Context) {
	d, ok := c.MustGet("model-serving-job").(ms.Deployment)
	if !ok || d.Contract.Code == nil {
		h.servingError(c, ms.ErrNotReady)
		return
	}
	snapshot := *d.Contract.Code
	if me.ValidateCodeSnapshot(snapshot) != nil {
		h.servingError(c, ms.ErrNotReady)
		return
	}
	done, ok := h.servingDownloadDeadline(c)
	if !ok {
		return
	}
	defer done()
	release, ok := h.acquireEvaluationCodeOperation(c)
	if !ok {
		return
	}
	defer release()
	reader, size, err := h.evaluationCode.Open(c.Request.Context(), snapshot)
	if err != nil {
		h.servingError(c, ms.ErrUnavailable)
		return
	}
	if reader == nil {
		h.servingError(c, ms.ErrUnavailable)
		return
	}
	defer reader.Close()
	if size != snapshot.SizeBytes {
		h.servingError(c, ms.ErrNotReady)
		return
	}
	// Open validates SHA256, size, and the immutable ZIP before exposing a reader.
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "serving-code-" + snapshot.ID + ".zip"}))
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Header("X-Content-SHA256", snapshot.SHA256)
	c.Header("Accept-Ranges", "none")
	n, err := io.CopyBuffer(c.Writer, io.LimitReader(reader, size), make([]byte, 64<<10))
	if err != nil || n != size {
		h.servingDownloadError(c, ms.ErrUnavailable)
	}
}
func (h *Handler) downloadServingModel(c *gin.Context) {
	d, ok := c.MustGet("model-serving-job").(ms.Deployment)
	if !ok || h.models == nil || h.modelSnapshots == nil {
		h.servingError(c, ms.ErrNotReady)
		return
	}
	done, ok := h.servingDownloadDeadline(c)
	if !ok {
		return
	}
	defer done()
	v, err := h.models.GetVersion(c.Request.Context(), d.ModelID, d.VersionID)
	if err != nil || v.ID != d.VersionID || v.ModelID != d.ModelID || v.State != ml.Ready || v.SHA256 != d.ModelSHA256 || v.FileName != d.FileName || v.SizeBytes != d.ModelSizeBytes || v.SizeBytes < 1 || !artifactSHAValid(d.ModelSHA256) {
		h.servingError(c, ms.ErrNotReady)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": v.FileName})
	if disposition == "" {
		h.servingError(c, ms.ErrNotReady)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Length", strconv.FormatInt(v.SizeBytes, 10))
	c.Header("X-Content-SHA256", d.ModelSHA256)
	c.Header("Accept-Ranges", "none")
	h.servingDownloadError(c, h.modelSnapshots.Download(c.Request.Context(), v, c.Writer))
}
