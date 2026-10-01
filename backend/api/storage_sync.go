package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	ss "ray-train-platform-backend/storagesync"
)

type StorageSyncMetadataStore interface {
	StorageSyncList(context.Context, string, string, string, int, string) (objectstore.StorageSyncObjectPage, error)
	StorageSyncHead(context.Context, string, string) (*objectstore.StorageSyncObject, error)
	StorageSyncReadURL(context.Context, string, string, string, string) (objectstore.StorageSyncRead, error)
}
type syncRateEntry struct {
	limiter *rate.Limiter
	last    time.Time
}
type StorageSyncHandler struct {
	manager  *ss.Manager
	resolver *AdminStorageResolver
	metadata StorageSyncMetadataStore
	key      []byte
	response Handler
	mu       sync.Mutex
	rates    map[string]syncRateEntry
}

func NewStorageSyncHandler(manager *ss.Manager, resolver *AdminStorageResolver, metadata StorageSyncMetadataStore, key []byte) *StorageSyncHandler {
	return &StorageSyncHandler{manager: manager, resolver: resolver, metadata: metadata, key: append([]byte(nil), key...), rates: map[string]syncRateEntry{}}
}
func (h *StorageSyncHandler) RegisterManagementRoutes(group *gin.RouterGroup) {
	g := group.Group("/admin/storage-sync", h.guard)
	g.GET("/spaces", h.spaces)
	g.GET("/plans", h.plans)
	g.POST("/plans", h.createPlan)
	g.PATCH("/plans/:id", h.updatePlan)
	g.POST("/previews", h.createPreview)
	g.GET("/previews/:id", h.preview)
	g.POST("/plans/:id/runs", h.start)
	g.GET("/runs", h.runs)
	g.GET("/runs/:id", h.run)
	g.GET("/runs/:id/files", h.files)
	for _, action := range []string{"pause", "resume", "cancel", "retry"} {
		g.POST("/runs/:id/"+action, h.control(action))
	}
	g.POST("/browse-requests", h.browse)
	g.GET("/browse-requests/:id", h.browseResult)
}
func (h *StorageSyncHandler) guard(c *gin.Context) {
	p, ok := auth.PrincipalFromContext(c.Request.Context())
	if !ok {
		p, ok = auth.PrincipalFromGin(c)
	}
	if !ok || p.Subject == "" {
		h.response.writeError(c, 401, "AUTH_REQUIRED", "请先登录")
		c.Abort()
		return
	}
	if !auth.IsInteractiveAuthType(p.AuthType) || !p.HasRole(domain.RoleSuperAdmin) {
		h.response.writeError(c, 403, "FORBIDDEN", "仅超级管理员可通过网页登录使用数据同步")
		c.Abort()
		return
	}
	if h.resolver == nil {
		h.fail(c, errStorageSyncUnavailable)
		c.Abort()
		return
	}
	if err := h.resolver.IsAuthorized(c.Request.Context(), p.Subject); err != nil {
		h.fail(c, ss.ErrForbidden)
		c.Abort()
		return
	}
	if !h.allow(p.Subject) {
		h.response.writeError(c, 429, "RATE_LIMITED", "操作频繁，请稍后重试")
		c.Abort()
		return
	}
	if h.manager == nil {
		h.fail(c, errStorageSyncUnavailable)
		c.Abort()
		return
	}
	c.Set("storageSyncActor", p.Subject)
	c.Header("Cache-Control", "no-store")
	c.Next()
}
func (h *StorageSyncHandler) allow(actor string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	entry, ok := h.rates[actor]
	if !ok {
		for key, value := range h.rates {
			if now.Sub(value.last) > 10*time.Minute {
				delete(h.rates, key)
			}
		}
		if len(h.rates) >= 1000 {
			return false
		}
		entry.limiter = rate.NewLimiter(10, 30)
	}
	entry.last = now
	h.rates[actor] = entry
	return entry.limiter.Allow()
}

var errStorageSyncUnavailable = errors.New("storage sync unavailable")

func (h *StorageSyncHandler) fail(c *gin.Context, err error) {
	status, code, message := 500, "STORAGE_SYNC_ERROR", "数据同步服务暂时不可用"
	switch {
	case errors.Is(err, ss.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "当前用户没有数据同步管理权限"
	case errors.Is(err, ss.ErrNotFound):
		status, code, message = 404, "STORAGE_SYNC_NOT_FOUND", "记录不存在"
	case errors.Is(err, ss.ErrLocked):
		status, code, message = 409, "STORAGE_SYNC_PATH_LOCKED", "所选目录正在被其他同步任务使用"
	case errors.Is(err, ss.ErrPreviewInvalid):
		status, code, message = 409, "STORAGE_SYNC_PREVIEW_EXPIRED", "预览已过期或目录发生变化，请重新预览"
	case errors.Is(err, ss.ErrStaleAttempt):
		status, code, message = 409, "STORAGE_SYNC_STALE_ATTEMPT", "执行凭据已失效"
	case errors.Is(err, ss.ErrConflict):
		status, code, message = 409, "STORAGE_SYNC_CONFLICT", "状态已变化或存在未结束的任务，请刷新后重试"
	case errors.Is(err, ss.ErrInvalid):
		status, code, message = 400, "STORAGE_SYNC_INVALID", "请检查所选目录、同步配置及请求参数"
	case errors.Is(err, errStorageSyncUnavailable):
		status, code, message = 503, "STORAGE_SYNC_UNAVAILABLE", "数据同步尚未配置完成"
	}
	h.response.writeError(c, status, code, message)
}
func (h *StorageSyncHandler) decode(c *gin.Context, out any) bool {
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		h.fail(c, ss.ErrInvalid)
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		h.fail(c, ss.ErrInvalid)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		h.fail(c, ss.ErrInvalid)
		return false
	}
	return true
}
func (h *StorageSyncHandler) send(c *gin.Context, status int, result any, err error) {
	if err != nil {
		h.fail(c, err)
		return
	}
	h.response.writeSuccess(c, status, result)
}
func (h *StorageSyncHandler) spaces(c *gin.Context) {
	items, err := h.resolver.Spaces(c.Request.Context(), c.GetString("storageSyncActor"))
	h.send(c, 200, gin.H{"items": items}, err)
}
func (h *StorageSyncHandler) plans(c *gin.Context) {
	all, err := h.manager.ListPlans(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	items := make([]ss.Plan, 0, len(all))
	for _, item := range all {
		if syncCanReadPlan(c.GetString("storageSyncActor"), item) {
			items = append(items, item)
		}
	}
	page, start, end, ok := syncPage(c, len(items))
	if !ok {
		h.fail(c, ss.ErrInvalid)
		return
	}
	page["items"] = items[start:end]
	h.send(c, 200, page, nil)
}
func (h *StorageSyncHandler) createPlan(c *gin.Context) {
	var req struct {
		Name   string    `json:"name"`
		Config ss.Config `json:"config"`
	}
	if !h.decode(c, &req) {
		return
	}
	result, err := h.manager.CreatePlan(c.Request.Context(), c.GetString("storageSyncActor"), req.Name, req.Config)
	h.send(c, 201, result, err)
}
func (h *StorageSyncHandler) updatePlan(c *gin.Context) {
	if !h.checkPlan(c, c.Param("id")) {
		return
	}
	var req struct {
		Revision int64     `json:"revision"`
		Name     string    `json:"name"`
		Enabled  *bool     `json:"enabled"`
		Config   ss.Config `json:"config"`
	}
	if !h.decode(c, &req) {
		return
	}
	if req.Enabled == nil {
		h.fail(c, ss.ErrInvalid)
		return
	}
	result, err := h.manager.UpdatePlan(c.Request.Context(), c.GetString("storageSyncActor"), c.Param("id"), req.Revision, req.Name, *req.Enabled, req.Config)
	h.send(c, 200, result, err)
}
func (h *StorageSyncHandler) createPreview(c *gin.Context) {
	var req struct {
		PlanID         string `json:"planId"`
		ConfigRevision int64  `json:"configRevision"`
	}
	if !h.decode(c, &req) || !h.checkPlan(c, req.PlanID) {
		return
	}
	result, err := h.manager.CreatePreview(c.Request.Context(), c.GetString("storageSyncActor"), req.PlanID, req.ConfigRevision)
	h.send(c, 202, result, err)
}
func (h *StorageSyncHandler) preview(c *gin.Context) {
	result, err := h.manager.GetPreview(c.Request.Context(), c.Param("id"))
	if err == nil && (result.Kind != "PREVIEW" || result.Actor != c.GetString("storageSyncActor")) {
		err = ss.ErrNotFound
	}
	h.send(c, 200, result, err)
}
func (h *StorageSyncHandler) start(c *gin.Context) {
	if !h.checkPlan(c, c.Param("id")) {
		return
	}
	var req ss.StartRequest
	if !h.decode(c, &req) {
		return
	}
	req.IdempotencyKey = c.GetHeader("Idempotency-Key")
	result, err := h.manager.Start(c.Request.Context(), c.GetString("storageSyncActor"), c.Param("id"), req)
	h.send(c, 202, result, err)
}
func (h *StorageSyncHandler) runs(c *gin.Context) {
	if id := c.Query("planId"); id != "" && !h.checkPlan(c, id) {
		return
	}
	all, err := h.manager.ListRuns(c.Request.Context(), c.Query("planId"))
	if err != nil {
		h.fail(c, err)
		return
	}
	items := make([]ss.Run, 0, len(all))
	for _, item := range all {
		if syncCanReadRun(c.GetString("storageSyncActor"), item) {
			items = append(items, item)
		}
	}
	page, start, end, ok := syncPage(c, len(items))
	if !ok {
		h.fail(c, ss.ErrInvalid)
		return
	}
	page["items"] = items[start:end]
	h.send(c, 200, page, nil)
}
func (h *StorageSyncHandler) run(c *gin.Context) {
	if !h.checkRun(c, c.Param("id")) {
		return
	}
	result, err := h.manager.GetRun(c.Request.Context(), c.Param("id"))
	h.send(c, 200, result, err)
}
func (h *StorageSyncHandler) control(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.checkRun(c, c.Param("id")) {
			return
		}
		var req struct{}
		if !h.decode(c, &req) {
			return
		}
		result, err := h.manager.Control(c.Request.Context(), c.GetString("storageSyncActor"), c.Param("id"), action)
		h.send(c, 202, result, err)
	}
}
func (h *StorageSyncHandler) browse(c *gin.Context) {
	var req struct {
		Location ss.Location `json:"location"`
		Cursor   string      `json:"cursor"`
		Limit    int         `json:"limit"`
	}
	if !h.decode(c, &req) {
		return
	}
	result, err := h.manager.CreateBrowse(c.Request.Context(), c.GetString("storageSyncActor"), req.Location, req.Cursor, req.Limit)
	h.send(c, 202, result, err)
}
func (h *StorageSyncHandler) browseResult(c *gin.Context) {
	result, err := h.manager.GetPreview(c.Request.Context(), c.Param("id"))
	if err == nil && (result.Kind != "BROWSE" || result.Actor != c.GetString("storageSyncActor")) {
		err = ss.ErrNotFound
	}
	h.send(c, 200, result, err)
}
func syncPage(c *gin.Context, total int) (gin.H, int, int, bool) {
	limit := 100
	var err error
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			return nil, 0, 0, false
		}
	}
	start := 0
	if raw := c.Query("cursor"); raw != "" {
		start, err = strconv.Atoi(raw)
		if err != nil || start < 0 || start > total {
			return nil, 0, 0, false
		}
	}
	end := start + limit
	if end > total {
		end = total
	}
	next := ""
	if end < total {
		next = strconv.Itoa(end)
	}
	return gin.H{"nextCursor": next, "total": total}, start, end, true
}
