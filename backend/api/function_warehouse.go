package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	fw "ray-train-platform-backend/functionwarehouse"
)

// FunctionWarehouseClient accepts a request-scoped, verified OAuth token. It
// must not retain that token or use caller-controlled upstream destinations.
type FunctionWarehouseClient interface {
	ListWarehouses(context.Context, string, fw.PageQuery) (fw.WarehousePage, error)
	GetWarehouse(context.Context, string, string) (fw.Warehouse, error)
	ListModelTypes(context.Context, string, string) ([]fw.ModelType, error)
}

func DefaultFunctionWarehouseClients() (map[fw.Environment]FunctionWarehouseClient, error) {
	clients := make(map[fw.Environment]FunctionWarehouseClient)
	for _, target := range fw.Environments() {
		client, err := fw.NewClient(target.Environment)
		if err != nil { return nil, err }
		clients[target.Environment] = client
	}
	return clients, nil
}

func (h *Handler) RegisterFunctionWarehouseRoutes(group *gin.RouterGroup) {
	g := group.Group("/function-warehouse")
	g.Use(auth.RequireInteractiveSession(false), h.modelGuard(false))
	g.GET("/environments", h.functionWarehouseEnvironments)
	g.GET("/:environment/warehouses", h.functionWarehouseList)
	g.GET("/:environment/warehouses/:warehouseId/model-types", h.functionWarehouseModelTypes)
}

func (h *Handler) functionWarehouseToken(c *gin.Context) (string, bool) {
	token, ok := auth.VerifiedOAuth2AccessToken(c.Request.Context())
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "WAREHOUSE_OAUTH_REQUIRED", "请通过公司 SSO 登录后访问功能仓")
		return "", false
	}
	return token, true
}

func (h *Handler) functionWarehouseEnvironments(c *gin.Context) {
	if _, ok := h.functionWarehouseToken(c); !ok { return }
	targets := make([]fw.Target, 0, len(h.functionWarehouses))
	for _, target := range fw.Environments() {
		if h.functionWarehouses[target.Environment] != nil { targets = append(targets, target) }
	}
	h.writeSuccess(c, http.StatusOK, gin.H{"items": targets})
}

func (h *Handler) functionWarehouseClient(c *gin.Context) (FunctionWarehouseClient, string, bool) {
	token, ok := h.functionWarehouseToken(c)
	if !ok { return nil, "", false }
	env := fw.Environment(c.Param("environment"))
	if env != fw.Production && env != fw.Development {
		h.writeError(c, 400, "WAREHOUSE_ENVIRONMENT_INVALID", "请选择正式或开发环境")
		return nil, "", false
	}
	client := h.functionWarehouses[env]
	if client == nil {
		h.writeError(c, 503, "WAREHOUSE_UNAVAILABLE", "功能仓连接尚未配置")
		return nil, "", false
	}
	return client, token, true
}

func (h *Handler) functionWarehouseList(c *gin.Context) {
	client, token, ok := h.functionWarehouseClient(c)
	if !ok { return }
	page, pageErr := strconv.Atoi(c.DefaultQuery("pageNum", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if pageErr != nil || sizeErr != nil || page < 1 || page > 10000 || size < 1 || size > 100 || len(c.Query("keywords")) > 200 || c.Query("groupId") != "" || c.Query("baseUrl") != "" {
		h.writeError(c, 400, "WAREHOUSE_QUERY_INVALID", "分页参数无效；团队和访问地址由平台环境配置决定")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := client.ListWarehouses(ctx, token, fw.PageQuery{PageNum: page, PageSize: size, Keywords: c.Query("keywords")})
	if h.functionWarehouseError(c, err) { return }
	h.writeSuccess(c, http.StatusOK, result)
}

func (h *Handler) functionWarehouseModelTypes(c *gin.Context) {
	client, token, ok := h.functionWarehouseClient(c)
	if !ok { return }
	if len(c.Param("warehouseId")) > 128 || c.Param("warehouseId") == "" {
		h.writeError(c, 400, "WAREHOUSE_ID_INVALID", "功能仓 ID 无效")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	warehouse, err := client.GetWarehouse(ctx, token, c.Param("warehouseId"))
	if h.functionWarehouseError(c, err) { return }
	items, err := client.ListModelTypes(ctx, token, warehouse.ID)
	if h.functionWarehouseError(c, err) { return }
	h.writeSuccess(c, http.StatusOK, gin.H{"warehouse": warehouse, "items": items})
}

func (h *Handler) functionWarehouseError(c *gin.Context, err error) bool {
	if err == nil { return false }
	switch {
	case errors.Is(err, fw.ErrUnauthorized):
		h.writeError(c, 401, "WAREHOUSE_REAUTH_REQUIRED", "功能仓授权已失效，请重新登录授权")
	case errors.Is(err, fw.ErrForbidden):
		h.writeError(c, 403, "WAREHOUSE_FORBIDDEN", "当前账号无权访问此功能仓或目标不属于所选环境的团队")
	case errors.Is(err, fw.ErrInvalid):
		h.writeError(c, 400, "WAREHOUSE_REQUEST_INVALID", "功能仓请求参数无效")
	case errors.Is(err, fw.ErrRateLimited):
		c.Header("Retry-After", "60")
		h.writeError(c, 429, "WAREHOUSE_RATE_LIMITED", "功能仓请求过于频繁，请稍后重试")
	default:
		h.writeError(c, 502, "WAREHOUSE_UPSTREAM_FAILED", "功能仓暂时无法响应，请稍后重试")
	}
	return true
}
