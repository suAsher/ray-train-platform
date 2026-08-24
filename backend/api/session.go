package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"

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
	group.GET("/cluster/gpu-metrics/history", h.clusterGPUHistory)
}

// GPUInventoryProvider is satisfied by the Prometheus client. It is an
// interface so a deployment without Prometheus degrades to a clear message.
type GPUInventoryProvider interface {
	QueryGPUInventory(ctx context.Context) (observability.GPUInventory, error)
}

type GPUHistoryProvider interface {
	QueryGPUHistory(ctx context.Context, window, nodeName string) (observability.GPUHistory, error)
}

var allowedGPUHistoryWindows = map[string]struct{}{
	"15m": {}, "1h": {}, "6h": {}, "24h": {}, "7d": {},
}

var gpuNodeQueryPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

func (h *Handler) requireGPUAdministrator(c *gin.Context) (auth.Principal, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if principal.AuthType != auth.AuthTypeOIDC && principal.AuthType != auth.AuthTypeLocal && principal.AuthType != auth.AuthTypeDemo {
		h.writeError(c, http.StatusForbidden, "INTERACTIVE_SESSION_REQUIRED", "该指标仅供交互式管理员会话访问")
		return auth.Principal{}, false
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "ADMIN_REQUIRED", "需要团队管理员或平台管理员权限")
		return auth.Principal{}, false
	}
	return principal, true
}

func validGPUNodeQuery(nodeName string) bool {
	nodeName = strings.TrimSpace(nodeName)
	return nodeName == "" || (len(nodeName) <= 253 && gpuNodeQueryPattern.MatchString(nodeName))
}

func (h *Handler) clusterGPUHistory(c *gin.Context) {
	if _, ok := h.requireGPUAdministrator(c); !ok {
		return
	}
	window := c.DefaultQuery("window", "1h")
	if _, ok := allowedGPUHistoryWindows[window]; !ok {
		h.writeError(c, http.StatusBadRequest, "GPU_HISTORY_WINDOW_INVALID", "时间范围仅支持 15m、1h、6h、24h 或 7d")
		return
	}
	nodeName := strings.TrimSpace(c.Query("node"))
	if !validGPUNodeQuery(nodeName) {
		h.writeError(c, http.StatusBadRequest, "GPU_NODE_INVALID", "节点名称格式无效")
		return
	}
	provider, ok := h.metrics.(GPUHistoryProvider)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "GPU_METRICS_UNAVAILABLE", "GPU 历史指标未配置")
		return
	}
	history, err := provider.QueryGPUHistory(c.Request.Context(), window, nodeName)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "GPU_HISTORY_QUERY_FAILED", "无法读取 GPU 历史指标，请稍后重试")
		return
	}
	h.writeSuccess(c, http.StatusOK, history)
}

func (h *Handler) clusterGPUMetrics(c *gin.Context) {
	principal, ok := h.requireGPUAdministrator(c)
	if !ok {
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
	inventory = visibleGPUInventory(inventory, principal)
	h.writeSuccess(c, http.StatusOK, inventory)
}

func visibleGPUInventory(inventory observability.GPUInventory, principal auth.Principal) observability.GPUInventory {
	if principal.HasRole(domain.RoleSuperAdmin) {
		return inventory
	}
	visible := inventory
	visible.Devices = append([]observability.GPUDevice(nil), inventory.Devices...)
	allowedNamespace := "tenant-" + sanitizeDNS(principal.TenantID)
	for index, device := range visible.Devices {
		if device.Namespace == "" || device.Namespace == allowedNamespace {
			continue
		}
		device.Namespace = ""
		device.PodName = ""
		device.ContainerName = ""
		visible.Devices[index] = device
	}
	return visible
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
