package api

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"time"
)

func (h *Handler) RegisterModelServingReadRoutes(g *gin.RouterGroup) {
	invoke := g.Group("", auth.RequireScopes(domain.PATScopeModelsInvoke), h.servingGuard(true))
	invoke.POST("/model-services/:serviceId/invocations", h.invokeModelService)
	r := g.Group("", auth.RequireScopes(domain.PATScopeJobsRead), h.servingGuard(false))
	r.GET("/model-serving-contracts", h.listModelServingContracts)
	r.GET("/model-services", h.listModelServices)
	r.GET("/model-services/:serviceId", h.getModelService)
	r.GET("/model-services/:serviceId/health", h.getModelServiceHealth)
}
func (h *Handler) RegisterModelServingManagementRoutes(g *gin.RouterGroup) {
	r := g.Group("", auth.RequireInteractiveSession(false), h.servingGuard(true))
	r.POST("/model-serving-contracts", h.createModelServingContract)
	r.PATCH("/model-serving-contracts/:contractId", h.updateModelServingContract)
	r.POST("/model-services/preflight", h.preflightModelService)
	r.POST("/model-services", h.createModelService)
	r.POST("/model-services/:serviceId/stop", h.stopModelService)
}
func (h *Handler) servingGuard(write bool) gin.HandlerFunc {
	limiter := newFixedWindowSourceArtifactLimiter(30, 120, 10000, time.Now)
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromGin(c)
		if !ok || p.Subject == "" || p.TenantID == "" {
			h.writeError(c, 401, "AUTH_REQUIRED", "请先登录平台")
			c.Abort()
			return
		}
		if p.IntegrationID != "" {
			h.writeError(c, 403, "SERVING_MEMBER_REQUIRED", "请使用平台成员身份")
			c.Abort()
			return
		}
		if h.modelServing == nil || h.models == nil || h.modelReleases == nil {
			h.servingError(c, ms.ErrUnavailable)
			c.Abort()
			return
		}
		if len(c.Request.URL.RawQuery) > 2048 {
			h.servingError(c, ms.ErrInvalid)
			c.Abort()
			return
		}
		for _, key := range []string{"serviceId", "contractId"} {
			if id := c.Param(key); id != "" && !evaluationIdentifier(id) {
				h.servingError(c, ms.ErrNotFound)
				c.Abort()
				return
			}
		}
		action := sourceArtifactActionComplete
		if write {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(p.TenantID+"\x00"+p.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "操作过于频繁，请稍后重试")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
func (h *Handler) servingError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ms.ErrInvalid):
		h.writeError(c, 400, "INVALID_SERVING_REQUEST", "服务参数、固定来源或推理契约无效")
	case errors.Is(err, ms.ErrNotFound):
		h.writeError(c, 404, "SERVING_NOT_FOUND", "推理服务或方案不存在")
	case errors.Is(err, ms.ErrUnauthorized):
		h.writeError(c, 403, "SERVING_FORBIDDEN", "无权执行此服务操作")
	case errors.Is(err, ms.ErrConflict):
		h.writeError(c, 409, "SERVING_CONFLICT", "服务已变更或请求键冲突，请刷新后重试")
	case errors.Is(err, ms.ErrNotReady):
		h.writeError(c, 409, "SERVING_NOT_READY", "批准版本、权重或方案尚未就绪，请先完成发布审核")
	case errors.Is(err, ms.ErrQuota):
		h.writeError(c, 409, "SERVING_ACTIVE_LIMIT", "此模型已有运行中或正在回收的服务，请先停止并等待资源回收")
	default:
		h.writeError(c, 503, "SERVING_UNAVAILABLE", "推理服务暂不可用，请保留请求键后重试")
	}
	return true
}

type serviceResponse struct {
	ms.Deployment
	CanManage  bool   `json:"canManage"`
	Endpoint   string `json:"endpoint"`
	ContractID string `json:"contractId"`
	TTLSeconds int64  `json:"ttlSeconds"`
}

func canManageService(p auth.Principal, d ms.Deployment) bool {
	return p.Subject == d.OwnerID || p.HasRole(domain.RoleSuperAdmin)
}
func servingResponse(p auth.Principal, d ms.Deployment) serviceResponse {
	return serviceResponse{d, canManageService(p, d), "/api/v1/model-services/" + d.ID + "/invocations", d.Contract.ID, int64(d.ExpiresAt.Sub(d.CreatedAt) / time.Second)}
}
func (h *Handler) serviceForMember(c *gin.Context, manage bool) (ms.Deployment, bool) {
	d, err := h.modelServing.GetDeployment(c.Request.Context(), c.Param("serviceId"))
	if h.servingError(c, err) {
		return d, false
	}
	if manage && !canManageService(actorPrincipal(c), d) {
		h.servingError(c, ms.ErrUnauthorized)
		return d, false
	}
	return d, true
}
func (h *Handler) listModelServices(c *gin.Context) {
	f := ms.Filter{ModelID: c.Query("modelId"), State: c.Query("state"), Cursor: c.Query("cursor"), Limit: 100}
	p := actorPrincipal(c)
	if c.Query("mine") == "true" {
		f.OwnerID = p.Subject
	}
	page, err := h.modelServing.ListDeployments(c.Request.Context(), f)
	if h.servingError(c, err) {
		return
	}
	items := []serviceResponse{}
	for _, d := range page.Items {
		items = append(items, servingResponse(p, d))
	}
	h.writeSuccess(c, 200, gin.H{"items": items, "nextCursor": page.NextCursor})
}
func (h *Handler) getModelService(c *gin.Context) {
	d, ok := h.serviceForMember(c, false)
	if ok {
		h.writeSuccess(c, 200, servingResponse(actorPrincipal(c), d))
	}
}
func (h *Handler) stopModelService(c *gin.Context) {
	d, ok := h.serviceForMember(c, true)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if !h.decodeEvaluationJSON(c, &input, map[string]bool{"revision": true}) {
		return
	}
	stopped, err := h.modelServing.RequestStop(c.Request.Context(), d.ID, actorPrincipal(c).Subject, input.Revision)
	if h.servingError(c, err) {
		return
	}
	// The reservation/store and job creation use the same transaction fence.
	// The controller retries cancellation, including a lost API response.
	h.writeSuccess(c, 202, servingResponse(actorPrincipal(c), stopped))
}
