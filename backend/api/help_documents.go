package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type HelpDocumentStore interface {
	ListHelpDocuments(context.Context, bool) ([]domain.HelpDocument, error)
	CreateHelpDocument(context.Context, domain.HelpDocument, string) (domain.HelpDocument, error)
	ChangeHelpDocument(context.Context, string, int64, string, int64, *domain.HelpDocument, string) (domain.HelpDocument, error)
	HelpDocumentHistory(context.Context, string) ([]domain.HelpDocument, error)
}

func (h *Handler) helpGuard(admin bool) gin.HandlerFunc {
	// Per-handler, bounded per-principal limiter; both reads and writes are capped.
	limiter := newFixedWindowSourceArtifactLimiter(30, 120, 10000, time.Now)
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromGin(c)
		if !ok {
			h.writeError(c, 401, "AUTH_REQUIRED", "请先登录")
			c.Abort()
			return
		}
		if admin && (!p.HasRole(domain.RoleSuperAdmin) || !auth.IsInteractiveAuthType(p.AuthType)) {
			h.writeError(c, 403, "FORBIDDEN", "仅超级管理员登录会话可管理文档")
			c.Abort()
			return
		}
		action := sourceArtifactActionComplete
		if c.Request.Method != "GET" {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(p.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "操作过于频繁，请稍后重试")
			c.Abort()
			return
		}
		if h.helpDocuments == nil {
			h.writeError(c, 503, "HELP_UNAVAILABLE", "文档服务暂不可用")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
func (h *Handler) RegisterHelpReadRoutes(group *gin.RouterGroup) {
	group.Group("", h.helpGuard(false)).GET("/help/documents", func(c *gin.Context) { h.listHelp(c, false) })
}
func (h *Handler) RegisterHelpManagementRoutes(group *gin.RouterGroup) {
	admin := group.Group("/admin/help/documents", h.helpGuard(true))
	admin.GET("", func(c *gin.Context) { h.listHelp(c, true) })
	admin.POST("", h.createHelp)
	admin.PUT("/:id", func(c *gin.Context) { h.changeHelp(c, "save") })
	admin.POST("/:id/publish", func(c *gin.Context) { h.changeHelp(c, "publish") })
	admin.POST("/:id/unpublish", func(c *gin.Context) { h.changeHelp(c, "unpublish") })
	admin.POST("/:id/restore", func(c *gin.Context) { h.changeHelp(c, "restore") })
	admin.GET("/:id/history", h.helpHistory)
}
func (h *Handler) helpError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repositories.ErrHelpConflict):
		h.writeError(c, 409, "HELP_CONFLICT", "文档已被修改或 ID 已存在。请保留本地内容并重新加载后合并。")
	case errors.Is(err, repositories.ErrHelpNotFound):
		h.writeError(c, 404, "HELP_NOT_FOUND", "文档或历史版本不存在")
	default:
		h.writeError(c, 500, "HELP_FAILED", "文档操作失败，请稍后重试")
	}
}
func (h *Handler) listHelp(c *gin.Context, admin bool) {
	items, err := h.helpDocuments.ListHelpDocuments(c.Request.Context(), admin)
	if err != nil {
		h.helpError(c, err)
		return
	}
	h.writeSuccess(c, 200, gin.H{"items": items})
}

type helpWriteRequest struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Category        string `json:"category"`
	SortOrder       int    `json:"sortOrder"`
	Markdown        string `json:"markdown"`
	ExpectedVersion int64  `json:"expectedVersion"`
	RestoreVersion  int64  `json:"restoreVersion"`
}

func (h *Handler) bindHelp(c *gin.Context) (helpWriteRequest, bool) {
	var req helpWriteRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 600*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("trailing JSON")
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(c, 413, "HELP_TOO_LARGE", "文档请求过大")
		} else {
			h.writeError(c, 400, "INVALID_JSON", "文档请求格式不正确")
		}
		return req, false
	}
	return req, true
}
func (h *Handler) createHelp(c *gin.Context) {
	req, ok := h.bindHelp(c)
	if !ok {
		return
	}
	d := domain.HelpDocument{ID: req.ID, Title: req.Title, Category: req.Category, SortOrder: req.SortOrder, Markdown: req.Markdown}
	if err := d.Validate(); err != nil {
		h.writeError(c, 400, "INVALID_HELP", err.Error())
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	out, err := h.helpDocuments.CreateHelpDocument(c.Request.Context(), d, p.Subject)
	if err != nil {
		h.helpError(c, err)
		return
	}
	h.writeSuccess(c, 201, out)
}
func (h *Handler) changeHelp(c *gin.Context, action string) {
	req, ok := h.bindHelp(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if !domain.ValidHelpID(id) || req.ID != "" || req.ExpectedVersion < 1 || (action == "restore" && req.RestoreVersion < 1) {
		h.writeError(c, 400, "INVALID_HELP", "文档 ID 或版本不正确")
		return
	}
	d := domain.HelpDocument{ID: id, Title: req.Title, Category: req.Category, SortOrder: req.SortOrder, Markdown: req.Markdown}
	if action == "save" {
		if err := d.Validate(); err != nil {
			h.writeError(c, 400, "INVALID_HELP", err.Error())
			return
		}
	}
	p, _ := auth.PrincipalFromGin(c)
	out, err := h.helpDocuments.ChangeHelpDocument(c.Request.Context(), id, req.ExpectedVersion, action, req.RestoreVersion, &d, p.Subject)
	if err != nil {
		h.helpError(c, err)
		return
	}
	h.writeSuccess(c, 200, out)
}
func (h *Handler) helpHistory(c *gin.Context) {
	if !domain.ValidHelpID(c.Param("id")) {
		h.writeError(c, 400, "INVALID_HELP", "文档 ID 不正确")
		return
	}
	items, err := h.helpDocuments.HelpDocumentHistory(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.helpError(c, err)
		return
	}
	h.writeSuccess(c, 200, gin.H{"items": items})
}
