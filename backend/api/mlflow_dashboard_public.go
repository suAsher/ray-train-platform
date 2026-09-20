package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/repositories"
)

func (h *Handler) publicMLflowDashboardGuard(limiter SourceArtifactLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.mlflowDashboardPublicEnabled {
			if allowed, _ := limiter.Allow("mlflow-dashboard-anonymous", sourceArtifactActionCreate); !allowed {
				c.Header("Retry-After", "60")
				h.writeError(c, 429, "RATE_LIMITED", "MLflow request rate limit exceeded")
				c.Abort()
				return
			}
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}

func (h *Handler) proxyPublicMLflowDashboard(c *gin.Context) {
	// Preserve the existing Portal ticket URL contract. In public mode a ticket
	// is only a one-time navigation hint, never a prerequisite for access.
	if _, present := c.Request.URL.Query()["access_token"]; present {
		fragment := ""
		if h.mlflowDashboardStore != nil {
			hash := sha256.Sum256([]byte(c.Query("access_token")))
			if record, err := h.mlflowDashboardStore.ConsumeMLflowDashboardTicket(c.Request.Context(), hex.EncodeToString(hash[:]), h.mlflowDashboardNow()); err == nil {
				fragment = record.RedirectFragment
			}
		}
		c.Redirect(http.StatusFound, mlflowDashboardBasePath+fragment)
		return
	}
	if isMLflowMutation(c.Request.Method) && !h.hasValidMLflowMutationOrigin(c.Request) {
		h.writeError(c, 403, "MLFLOW_DASHBOARD_ORIGIN_FORBIDDEN", "MLflow Dashboard mutation origin is not allowed")
		return
	}
	target, err := parseMLflowNativeTarget(h.mlflowTrackingURL)
	if err != nil {
		h.writeError(c, 502, "MLFLOW_DASHBOARD_UPSTREAM_INVALID", "MLflow Dashboard upstream is invalid")
		return
	}
	h.serveAuditedPublicMLflowDashboard(c, target)
}

func (h *Handler) serveAuditedPublicMLflowDashboard(c *gin.Context, target *url.URL) {
	if h.mlflowDashboardStore == nil {
		h.writeError(c, 503, "MLFLOW_DASHBOARD_UNAVAILABLE", "MLflow Dashboard access is not configured")
		return
	}
	startedAt := time.Now()
	event := repositories.MLflowAuditEvent{
		Principal: auth.Principal{Subject: "mlflow-anonymous", AuthType: auth.AuthTypeAnonymous},
		Method:    c.Request.Method, Path: c.Request.URL.Path, RequestID: c.GetHeader("X-Request-ID"),
	}
	if isMLflowMutation(c.Request.Method) {
		event.Status = http.StatusProcessing
		if err := h.mlflowDashboardStore.CreateMLflowAuditLog(c.Request.Context(), event); err != nil {
			h.writeError(c, 503, "MLFLOW_DASHBOARD_AUDIT_UNAVAILABLE", "could not record MLflow access")
			return
		}
	}
	defer func() {
		event.Status = c.Writer.Status()
		event.Duration = time.Since(startedAt)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), mlflowNativeAuditTimeout)
		defer cancel()
		if err := h.mlflowDashboardStore.CreateMLflowAuditLog(ctx, event); err != nil {
			log.Printf("create public MLflow Dashboard audit log: %v", err)
		}
	}()
	h.serveMLflowDashboardProxy(c, target)
}
