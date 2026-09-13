package api

import (
	"errors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	mr "ray-train-platform-backend/modelrelease"
	"strconv"
	"strings"
)

type modelReleaseResponse struct {
	mr.Release
	CanReview       bool `json:"canReview"`
	CanPublish      bool `json:"canPublish"`
	CanReadEvidence bool `json:"canReadEvidence"`
}

func releaseActor(c *gin.Context) mr.Actor {
	p, _ := auth.PrincipalFromGin(c)
	return mr.Actor{ID: p.Subject, Name: p.Username, TenantID: p.TenantID, SuperAdmin: p.HasRole(domain.RoleSuperAdmin), TenantAdmin: p.HasRole(domain.RoleTenantAdmin)}
}
func releaseResponse(r mr.Release, a mr.Actor) modelReleaseResponse {
	evidence := r.DatasetVisibility == "PUBLIC" || a.SuperAdmin || (a.TenantID != "" && a.TenantID == r.DatasetTenantID)
	return modelReleaseResponse{Release: r, CanReview: r.State == mr.Pending && evidence && mr.CanReview(r, a), CanPublish: r.State == mr.Approved && (a.SuperAdmin || a.ID == r.ModelOwnerID), CanReadEvidence: evidence}
}
func (h *Handler) RegisterModelReleaseRoutes(group *gin.RouterGroup) {
	read := group.Group("", auth.RequireInteractiveSession(false), h.modelGuard(false), h.modelReleaseGuard())
	read.GET("/model-releases", h.listModelReleases)
	read.GET("/model-releases/:releaseId", h.getModelRelease)
	read.GET("/models/:modelId/publication", h.getModelPublication)
	read.GET("/models/:modelId/publication/history", h.listModelPublicationHistory)
	write := group.Group("", auth.RequireInteractiveSession(false), h.modelGuard(true), h.modelReleaseGuard())
	write.POST("/model-releases", h.createModelRelease)
	write.POST("/model-releases/:releaseId/decision", h.decideModelRelease)
	write.POST("/models/:modelId/publication", h.publishModelRelease)
}
func (h *Handler) modelReleaseGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.modelReleases == nil {
			h.writeError(c, 503, "MODEL_RELEASE_UNAVAILABLE", "模型发布服务暂不可用")
			c.Abort()
			return
		}
		if !validLifecycleRoute(c) {
			h.modelReleaseError(c, mr.ErrInvalid)
			c.Abort()
			return
		}
		c.Next()
	}
}
func validLifecycleRoute(c *gin.Context) bool {
	if len(c.Request.URL.RawQuery) > 2048 {
		return false
	}
	for _, key := range []string{"modelId", "versionId", "releaseId"} {
		if v := c.Param(key); v != "" && !evaluationIdentifier(v) {
			return false
		}
	}
	return true
}
func (h *Handler) modelReleaseError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, mr.ErrInvalid):
		h.writeError(c, 400, "MODEL_RELEASE_INVALID", "请检查版本、评估结果、原因及请求参数")
	case errors.Is(err, mr.ErrUnauthorized):
		h.writeError(c, 403, "MODEL_RELEASE_FORBIDDEN", "无权执行此操作；申请人和模型所有者不能审批自己的模型")
	case errors.Is(err, mr.ErrNotFound):
		h.writeError(c, 404, "MODEL_RELEASE_NOT_FOUND", "发布记录不存在或不可见")
	case errors.Is(err, mr.ErrConflict):
		h.writeError(c, 409, "MODEL_RELEASE_CONFLICT", "记录已发生变化，请刷新后重试")
	case errors.Is(err, mr.ErrNotReady):
		h.writeError(c, 409, "MODEL_RELEASE_NOT_READY", "需要已完成的权重版本和成功评估报告")
	default:
		h.writeError(c, 503, "MODEL_RELEASE_FAILED", "模型发布服务暂不可用")
	}
	return true
}
func (h *Handler) listModelReleases(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	f := mr.Filter{ModelID: c.Query("modelId"), VersionID: c.Query("versionId"), State: c.Query("state"), Cursor: c.Query("cursor"), Limit: limit}
	for _, id := range []string{f.ModelID, f.VersionID, f.Cursor} {
		if id != "" && !evaluationIdentifier(id) {
			h.modelReleaseError(c, mr.ErrInvalid)
			return
		}
	}
	a := releaseActor(c)
	page, err := h.modelReleases.ListReleases(c.Request.Context(), f, a)
	if h.modelReleaseError(c, err) {
		return
	}
	items := make([]modelReleaseResponse, 0, len(page.Items))
	for _, r := range page.Items {
		items = append(items, releaseResponse(r, a))
	}
	h.writeSuccess(c, 200, gin.H{"items": items, "nextCursor": page.NextCursor})
}
func (h *Handler) getModelRelease(c *gin.Context) {
	a := releaseActor(c)
	r, err := h.modelReleases.GetRelease(c.Request.Context(), c.Param("releaseId"), a)
	if h.modelReleaseError(c, err) {
		return
	}
	h.writeSuccess(c, 200, releaseResponse(r, a))
}
func (h *Handler) createModelRelease(c *gin.Context) {
	key, ok := h.modelRequestKey(c)
	if !ok {
		return
	}
	var input mr.Request
	if !h.decodeModelJSON(c, &input) {
		return
	}
	input.IdempotencyKey = key
	if !evaluationIdentifier(input.ModelID) || !evaluationIdentifier(input.VersionID) || !evaluationIdentifier(input.EvaluationID) {
		h.modelReleaseError(c, mr.ErrInvalid)
		return
	}
	a := releaseActor(c)
	r, err := h.modelReleases.CreateRequest(c.Request.Context(), input, a)
	if h.modelReleaseError(c, err) {
		return
	}
	h.writeSuccess(c, 201, releaseResponse(r, a))
}
func (h *Handler) decideModelRelease(c *gin.Context) {
	var input mr.Decision
	if !h.decodeModelJSON(c, &input) {
		return
	}
	if input.Revision < 1 || !mr.ValidReason(input.Reason) {
		h.modelReleaseError(c, mr.ErrInvalid)
		return
	}
	a := releaseActor(c)
	r, err := h.modelReleases.GetRelease(c.Request.Context(), c.Param("releaseId"), a)
	if h.modelReleaseError(c, err) {
		return
	}
	if !releaseResponse(r, a).CanReview {
		h.modelReleaseError(c, mr.ErrUnauthorized)
		return
	}
	r, err = h.modelReleases.Decide(c.Request.Context(), r.ID, input, a)
	if h.modelReleaseError(c, err) {
		return
	}
	h.writeSuccess(c, 200, releaseResponse(r, a))
}
func (h *Handler) getModelPublication(c *gin.Context) {
	p, err := h.modelReleases.GetPublication(c.Request.Context(), c.Param("modelId"), releaseActor(c))
	if h.modelReleaseError(c, err) {
		return
	}
	h.writeSuccess(c, 200, p)
}
func (h *Handler) publishModelRelease(c *gin.Context) {
	key, ok := h.modelRequestKey(c)
	if !ok {
		return
	}
	var input mr.PublishRequest
	if !h.decodeModelJSON(c, &input) {
		return
	}
	input.IdempotencyKey = key
	if !evaluationIdentifier(input.ReleaseID) || input.Revision < 0 || !mr.ValidReason(input.Reason) {
		h.modelReleaseError(c, mr.ErrInvalid)
		return
	}
	p, err := h.modelReleases.Publish(c.Request.Context(), c.Param("modelId"), input, releaseActor(c))
	if h.modelReleaseError(c, err) {
		return
	}
	h.writeSuccess(c, 200, p)
}
func (h *Handler) listModelPublicationHistory(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" && !evaluationIdentifier(cursor) {
		h.modelReleaseError(c, mr.ErrInvalid)
		return
	}
	page, err := h.modelReleases.ListHistory(c.Request.Context(), c.Param("modelId"), cursor, limit, releaseActor(c))
	if h.modelReleaseError(c, err) {
		return
	}
	h.writeSuccess(c, 200, page)
}
