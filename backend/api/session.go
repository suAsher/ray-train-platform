package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

const demoAuthType = auth.AuthTypeDemo

// sessionResponse is the server's authoritative view of the caller. The Portal
// renders identity from this payload instead of decoding the OIDC token itself,
// so the tenant shown in the UI is always the tenant the API enforces.
type sessionResponse struct {
	Subject   string   `json:"subject"`
	Username  string   `json:"username"`
	Email     string   `json:"email"`
	TenantID  string   `json:"tenantId"`
	Roles     []string `json:"roles"`
	AuthType  string   `json:"authType"`
	Anonymous bool     `json:"anonymous"`
	Namespace string   `json:"namespace"`
	Queue     string   `json:"queue"`
}

func (h *Handler) RegisterSessionRoutes(group *gin.RouterGroup) {
	group.GET("/me", h.currentSession)
	group.GET("/quota", h.currentQuota)
	// Deployment ceilings and governed mount paths belong next to identity: the
	// Portal needs both before it can render a submit form the server accepts.
	group.GET("/limits", h.platformLimits)
	// Live GPU state from DCGM. The devices page previously showed only
	// Kubernetes GPU requests, which says what is reserved but not what is busy.
	group.GET("/cluster/gpu-metrics", h.clusterGPUMetrics)
}

// GPUInventoryProvider is satisfied by the Prometheus client. It is an
// interface so a deployment without Prometheus degrades to a clear message.
type GPUInventoryProvider interface {
	QueryGPUInventory(ctx context.Context) (observability.GPUInventory, error)
}

func (h *Handler) clusterGPUMetrics(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	provider, ok := h.metrics.(GPUInventoryProvider)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "GPU_METRICS_UNAVAILABLE", "GPU 指标未配置：需要 Prometheus 与 DCGM Exporter")
		return
	}
	inventory, err := provider.QueryGPUInventory(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "GPU_METRICS_QUERY_FAILED", "无法读取 GPU 指标，请稍后重试")
		return
	}
	h.writeSuccess(c, http.StatusOK, inventory)
}

// QuotaStore exposes the caller's own GPU budget. It is deliberately not an
// admin endpoint: an engineer needs to know how much capacity is left before
// sizing a job, without being able to see or change other tenants.
type QuotaStore interface {
	TenantGPUQuota(ctx context.Context, tenantID string) (domain.TenantQuota, error)
}

func (h *Handler) currentQuota(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.quota == nil {
		h.writeError(c, http.StatusServiceUnavailable, "QUOTA_UNAVAILABLE", "quota information is not configured")
		return
	}
	quota, err := h.quota.TenantGPUQuota(c.Request.Context(), principal.TenantID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "QUOTA_QUERY_FAILED", "could not read tenant quota")
		return
	}
	h.writeSuccess(c, http.StatusOK, quota)
}

func (h *Handler) currentSession(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	roles := principal.Roles
	if roles == nil {
		roles = []string{}
	}
	h.writeSuccess(c, http.StatusOK, sessionResponse{
		Subject:   principal.Subject,
		Username:  principal.Username,
		Email:     principal.Email,
		TenantID:  principal.TenantID,
		Roles:     roles,
		AuthType:  string(principal.AuthType),
		Anonymous: principal.AuthType == "" || principal.AuthType == demoAuthType,
		Namespace: "tenant-" + sanitizeDNS(principal.TenantID),
		Queue:     tenantQueue(principal.TenantID),
	})
}
