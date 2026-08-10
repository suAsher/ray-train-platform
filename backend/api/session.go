package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
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
