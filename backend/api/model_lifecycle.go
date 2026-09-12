package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/modellifecycle"
)

type modelSnapshotService interface {
	RequestVersion(context.Context, modellifecycle.VersionRequest) (modellifecycle.Version, error)
	Download(context.Context, modellifecycle.Version, io.Writer) error
}
type modelResponse struct {
	modellifecycle.Model
	CanManage bool `json:"canManage"`
}
type modelVersionResponse struct {
	modellifecycle.Version
	CanManage bool `json:"canManage"`
}

func (h *Handler) RegisterModelReadRoutes(group *gin.RouterGroup) {
	read := group.Group("")
	read.Use(auth.RequireScopes(domain.PATScopeJobsRead), h.modelGuard(false))
	read.GET("/models", h.listModels)
	read.GET("/models/:modelId", h.getModel)
	read.GET("/models/:modelId/versions", h.listModelVersions)
	read.GET("/models/:modelId/versions/:versionId", h.getModelVersion)
	read.GET("/models/:modelId/versions/:versionId/download", h.downloadModelVersion)
}

// The caller mounts management on the interactive-session group. Native MLflow
// keeps its separate, explicitly authorized full-access protocol unchanged.
func (h *Handler) RegisterModelManagementRoutes(group *gin.RouterGroup) {
	write := group.Group("")
	write.Use(auth.RequireInteractiveSession(h.allowAnonymous), h.modelGuard(true))
	write.POST("/models", h.createModel)
	write.PATCH("/models/:modelId", h.updateModel)
	write.POST("/models/:modelId/versions", h.createModelVersion)
	write.PATCH("/models/:modelId/versions/:versionId", h.updateModelVersion)
}
func (h *Handler) modelGuard(write bool) gin.HandlerFunc {
	limiter := newFixedWindowSourceArtifactLimiter(30, 120, 10000, time.Now)
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromGin(c)
		if !ok || p.Subject == "" || p.TenantID == "" {
			h.writeError(c, 401, "AUTH_REQUIRED", "请先登录平台")
			c.Abort()
			return
		}
		if p.IntegrationID != "" {
			h.writeError(c, 403, "MODEL_SESSION_REQUIRED", "请使用平台成员身份访问模型目录")
			c.Abort()
			return
		}
		if h.models == nil {
			h.writeError(c, 503, "MODELS_UNAVAILABLE", "模型目录暂不可用")
			c.Abort()
			return
		}
		if len(c.Query("q")) > 200 || len(c.Query("cursor")) > 128 || len(c.Param("modelId")) > 128 || len(c.Param("versionId")) > 128 {
			h.writeError(c, 400, "INVALID_MODEL_REQUEST", "查询参数过长")
			c.Abort()
			return
		}
		action := sourceArtifactActionComplete
		if write {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(p.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "操作过于频繁，请稍后重试")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
func canManageModel(p auth.Principal, m modellifecycle.Model) bool {
	return p.Subject == m.OwnerID || p.HasRole(domain.RoleSuperAdmin)
}
func modelActor(p auth.Principal) modellifecycle.Actor {
	return modellifecycle.Actor{ID: p.Subject, Name: p.Username}
}
func (h *Handler) modelForRequest(c *gin.Context, manage bool) (modellifecycle.Model, bool) {
	m, err := h.models.GetModel(c.Request.Context(), c.Param("modelId"))
	if h.modelError(c, err) {
		return m, false
	}
	p, _ := auth.PrincipalFromGin(c)
	if manage && !canManageModel(p, m) {
		h.writeError(c, 403, "MODEL_FORBIDDEN", "仅模型创建者或平台管理员可以管理此模型")
		return m, false
	}
	return m, true
}
func (h *Handler) modelError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, modellifecycle.ErrNotFound):
		h.writeError(c, 404, "MODEL_NOT_FOUND", "模型或版本不存在")
	case errors.Is(err, modellifecycle.ErrInvalid):
		h.writeError(c, 400, "INVALID_MODEL_REQUEST", "请检查名称、说明、版本、文件路径和请求参数")
	case errors.Is(err, modellifecycle.ErrConflict):
		h.writeError(c, 409, "MODEL_CONFLICT", "记录已更新、模型已归档或请求键冲突，请刷新后重试")
	case errors.Is(err, modellifecycle.ErrQuota):
		h.writeError(c, 409, "MODEL_SNAPSHOT_QUOTA", "模型快照额度或待处理数量已达上限，请联系平台管理员")
	case errors.Is(err, modellifecycle.ErrNotReady):
		h.writeError(c, 409, "MODEL_SNAPSHOT_NOT_READY", "权重尚未完成复制校验，请稍后查看状态")
	default:
		h.writeError(c, 503, "MODEL_OPERATION_FAILED", "模型服务暂不可用，请稍后重试")
	}
	return true
}
func (h *Handler) decodeModelJSON(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(v)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = modellifecycle.ErrInvalid
		}
	}
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			h.writeError(c, 413, "MODEL_BODY_TOO_LARGE", "提交内容过大")
		} else {
			h.writeError(c, 400, "INVALID_MODEL_REQUEST", "提交内容或字段无效")
		}
		return false
	}
	return true
}
func (h *Handler) listModels(c *gin.Context) {
	p, _ := auth.PrincipalFromGin(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	f := modellifecycle.Filter{Q: strings.TrimSpace(c.Query("q")), Archived: c.Query("archived") == "true", Cursor: c.Query("cursor"), Limit: limit}
	if c.Query("mine") == "true" {
		f.OwnerID = p.Subject
	}
	page, err := h.models.ListModels(c.Request.Context(), f)
	if h.modelError(c, err) {
		return
	}
	items := make([]modelResponse, 0, len(page.Items))
	for _, m := range page.Items {
		items = append(items, modelResponse{m, canManageModel(p, m)})
	}
	h.writeSuccess(c, 200, gin.H{"items": items, "nextCursor": page.NextCursor})
}
func (h *Handler) getModel(c *gin.Context) {
	m, ok := h.modelForRequest(c, false)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	h.writeSuccess(c, 200, modelResponse{m, canManageModel(p, m)})
}
func (h *Handler) createModel(c *gin.Context) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !h.decodeModelJSON(c, &input) {
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	m, err := h.models.CreateModel(c.Request.Context(), modellifecycle.Model{Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), OwnerID: p.Subject, OwnerName: p.Username, TenantID: p.TenantID})
	if h.modelError(c, err) {
		return
	}
	h.writeSuccess(c, 201, modelResponse{m, true})
}
func (h *Handler) updateModel(c *gin.Context) {
	_, ok := h.modelForRequest(c, true)
	if !ok {
		return
	}
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Archived    *bool   `json:"archived"`
		Revision    int64   `json:"revision"`
	}
	if !h.decodeModelJSON(c, &input) {
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	m, err := h.models.UpdateModel(c.Request.Context(), c.Param("modelId"), modellifecycle.ModelUpdate{Name: input.Name, Description: input.Description, Archived: input.Archived, Revision: input.Revision}, modelActor(p))
	if h.modelError(c, err) {
		return
	}
	h.writeSuccess(c, 200, modelResponse{m, true})
}
func (h *Handler) listModelVersions(c *gin.Context) {
	m, ok := h.modelForRequest(c, false)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	page, err := h.models.ListVersions(c.Request.Context(), m.ID, c.Query("cursor"), limit)
	if h.modelError(c, err) {
		return
	}
	items := make([]modelVersionResponse, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, modelVersionResponse{v, canManageModel(p, m)})
	}
	h.writeSuccess(c, 200, gin.H{"items": items, "nextCursor": page.NextCursor})
}
func (h *Handler) getModelVersion(c *gin.Context) {
	m, ok := h.modelForRequest(c, false)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	v, err := h.models.GetVersion(c.Request.Context(), m.ID, c.Param("versionId"))
	if h.modelError(c, err) {
		return
	}
	h.writeSuccess(c, 200, modelVersionResponse{v, canManageModel(p, m)})
}
func (h *Handler) updateModelVersion(c *gin.Context) {
	m, ok := h.modelForRequest(c, true)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	var input struct {
		Description string `json:"description"`
		Revision    int64  `json:"revision"`
	}
	if !h.decodeModelJSON(c, &input) {
		return
	}
	v, err := h.models.UpdateVersion(c.Request.Context(), m.ID, c.Param("versionId"), modellifecycle.VersionUpdate{Description: input.Description, Revision: input.Revision}, modelActor(p))
	if h.modelError(c, err) {
		return
	}
	h.writeSuccess(c, 200, modelVersionResponse{v, true})
}
