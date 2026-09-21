package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	eb "ray-train-platform-backend/environmentbuild"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/registryauth"
)

type environmentBuildHandler struct {
	service *eb.Service
	limiter SourceArtifactLimiter
}

func RegisterEnvironmentBuildRoutes(group *gin.RouterGroup, service *eb.Service) {
	h := environmentBuildHandler{service: service, limiter: newFixedWindowSourceArtifactLimiter(20, 120, 10000, time.Now)}
	routes := group.Group("", h.authenticate)
	routes.GET("/environment-build-capabilities", func(c *gin.Context) { h.success(c, service.Capabilities()) })
	routes.POST("/registry-authorizations", h.createAuthorization)
	routes.GET("/registry-authorizations/:id/projects", h.projects)
	routes.POST("/registry-authorizations/:id/check-target", h.checkTarget)
	routes.DELETE("/registry-authorizations/:id", h.revoke)
	routes.POST("/workspaces/:id/environment-builds", h.create)
	routes.GET("/environment-builds", h.list)
	routes.GET("/environment-builds/:id", h.get)
	routes.POST("/environment-builds/:id/retry", h.retry)
	routes.POST("/environment-builds/:id/cancel", h.cancel)
	routes.GET("/environment-versions", h.versions)
}
func (h environmentBuildHandler) authenticate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	p, ok := auth.PrincipalFromGin(c)
	if !ok {
		h.failure(c, 401, "AUTH_REQUIRED", "请先登录平台")
		c.Abort()
		return
	}
	if !auth.IsInteractiveAuthType(p.AuthType) || !p.Allowed("Engineer") || p.Subject == "" || p.TenantID == "" || p.IntegrationID != "" {
		h.failure(c, 403, "INTERACTIVE_LOGIN_REQUIRED", "请使用本人交互登录会话操作训练环境")
		c.Abort()
		return
	}
	if !h.service.Enabled() && c.Request.URL.Path != "/api/v1/environment-build-capabilities" {
		h.failure(c, 503, "ENVIRONMENT_BUILD_UNAVAILABLE", "环境构建尚未启用")
		c.Abort()
		return
	}
	action := sourceArtifactActionCreate
	if c.Request.Method == http.MethodGet {
		action = sourceArtifactActionComplete
	}
	if allowed, _ := h.limiter.Allow(p.TenantID+"\x00"+p.Subject, action); !allowed {
		c.Header("Retry-After", "60")
		h.failure(c, 429, "RATE_LIMITED", "操作过于频繁，请稍后重试")
		c.Abort()
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}
func environmentOwner(c *gin.Context) eb.Owner {
	p, _ := auth.PrincipalFromGin(c)
	return eb.Owner{TenantID: p.TenantID, UserID: p.Subject}
}
func (h environmentBuildHandler) bind(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil || !environmentBuildObject(body, value) {
		h.failure(c, 400, "INVALID_JSON", "请检查表单内容，不允许重复字段")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		h.failure(c, 400, "INVALID_JSON", "请检查表单内容")
		return false
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		h.failure(c, 400, "INVALID_JSON", "请求只允许一个 JSON 对象")
		return false
	}
	return true
}
func (h environmentBuildHandler) success(c *gin.Context, data any) {
	c.JSON(200, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), data))
}
func (h environmentBuildHandler) failure(c *gin.Context, status int, code, message string) {
	c.JSON(status, httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")), code, message))
}
func (h environmentBuildHandler) result(c *gin.Context, value any, err error) {
	if err == nil {
		h.success(c, value)
		return
	}
	var phase *eb.PhaseError
	if errors.As(err, &phase) && phase.UserMessage() != "" {
		h.failure(c, 409, "ENVIRONMENT_BUILD_PRECHECK", phase.UserMessage())
		return
	}
	switch {
	case errors.Is(err, eb.ErrNotFound):
		h.failure(c, 404, "ENVIRONMENT_NOT_FOUND", "记录不存在或不属于当前用户")
	case errors.Is(err, eb.ErrInvalid):
		h.failure(c, 400, "ENVIRONMENT_BUILD_INVALID", "请检查仓库、名称、可见范围和操作标识")
	case errors.Is(err, eb.ErrCredentialCapacity):
		h.failure(c, 409, "REGISTRY_AUTHORIZATION_CAPACITY", "临时 Harbor 授权名额已满：每人最多 5 项、平台最多 50 项。请撤销不再使用的授权，或等待当前发布完成后重试")
	case errors.Is(err, eb.ErrCapacity):
		h.failure(c, 409, "ENVIRONMENT_BUILD_CAPACITY", "环境构建暂存空间名额已满：每人最多 3 项、平台最多 8 项。请等待完成，或取消不再需要的构建以释放暂存空间")
	case errors.Is(err, eb.ErrConflict):
		h.failure(c, 409, "ENVIRONMENT_BUILD_CONFLICT", "当前状态不支持此操作；请刷新，确认来源为本人运行中的配套 Base 调试环境")
	case errors.Is(err,registryauth.ErrProjectsUnavailable):
		h.failure(c,503,"REGISTRY_PROJECTS_UNAVAILABLE","Harbor 项目列表暂不可用，可手动输入已有项目并验证目标仓库写权限；无需因此更换凭据")
	case errors.Is(err, eb.ErrAuthorization):
		h.failure(c, 403, "REGISTRY_AUTHORIZATION_REQUIRED", "Harbor 凭据失效或没有目标仓库写权限，请使用用户名和 CLI Secret 重新授权")
	default:
		h.failure(c, 503, "ENVIRONMENT_BUILD_UNAVAILABLE", "环境服务暂不可用，请稍后重试")
	}
}
func (h environmentBuildHandler) createAuthorization(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	if !h.bind(c, &request) {
		return
	}
	a, err := h.service.CreateAuthorization(c.Request.Context(), environmentOwner(c), eb.Credentials{Username: request.Username, Secret: request.Secret})
	request.Secret = ""
	h.result(c, a, err)
}
func (h environmentBuildHandler) projects(c *gin.Context) {
	page := 1
	if raw := c.Query("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			h.result(c, nil, eb.ErrInvalid)
			return
		}
		page = n
	}
	items, err := h.service.Projects(c.Request.Context(), environmentOwner(c), c.Param("id"), page)
	next := 0
	if len(items) == 50 {
		next = page + 1
	}
	h.result(c, map[string]any{"items": items, "nextPage": next}, err)
}
func (h environmentBuildHandler) checkTarget(c *gin.Context) {
	var request struct {
		Project    string `json:"project"`
		Repository string `json:"repository"`
	}
	if !h.bind(c, &request) {
		return
	}
	err := h.service.CheckTarget(c.Request.Context(), environmentOwner(c), c.Param("id"), request.Project, request.Repository)
	h.result(c, map[string]any{"allowed": err == nil, "repository": request.Project + "/" + request.Repository}, err)
}
func (h environmentBuildHandler) revoke(c *gin.Context) {
	err := h.service.RevokeAuthorization(c.Request.Context(), environmentOwner(c), c.Param("id"))
	h.result(c, map[string]bool{"revoked": err == nil}, err)
}
func (h environmentBuildHandler) create(c *gin.Context) {
	var request eb.CreateRequest
	if !h.bind(c, &request) {
		return
	}
	b, err := h.service.Create(c.Request.Context(), environmentOwner(c), c.Param("id"), request)
	h.result(c, b, err)
}
func (h environmentBuildHandler) list(c *gin.Context) {
	items, err := h.service.List(c.Request.Context(), environmentOwner(c))
	h.result(c, map[string]any{"items": items}, err)
}
func (h environmentBuildHandler) get(c *gin.Context) {
	b, err := h.service.Get(c.Request.Context(), environmentOwner(c), c.Param("id"))
	h.result(c, b, err)
}
func (h environmentBuildHandler) retry(c *gin.Context) {
	var request struct {
		AuthorizationID string `json:"authorizationId"`
	}
	if !h.bind(c, &request) {
		return
	}
	b, err := h.service.Retry(c.Request.Context(), environmentOwner(c), c.Param("id"), request.AuthorizationID)
	h.result(c, b, err)
}
func (h environmentBuildHandler) cancel(c *gin.Context) {
	b, err := h.service.Cancel(c.Request.Context(), environmentOwner(c), c.Param("id"))
	h.result(c, b, err)
}
func (h environmentBuildHandler) versions(c *gin.Context) {
	items, err := h.service.Versions(c.Request.Context(), environmentOwner(c))
	h.result(c, map[string]any{"items": items}, err)
}

// Exact JSON field names and duplicate rejection prevent ambiguous credential
// and target requests from passing through different proxy/parser behavior.
func environmentBuildObject(body []byte, value any) bool {
	allowed := map[string]bool{}
	shape := reflect.TypeOf(value).Elem()
	for i := 0; i < shape.NumField(); i++ {
		allowed[strings.Split(shape.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return false
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}')
}
