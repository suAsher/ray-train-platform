package api

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type DatasetCleanupStore interface {
	DeleteFailedDatasetRecord(context.Context, string, bool, string, string, string, bool) error
}

var cleanupDatasetID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

func (h *Handler) registerDatasetCleanupRoutes(group *gin.RouterGroup) {
	limiter := newFixedWindowSourceArtifactLimiter(120, 120, 10000, time.Now)
	cleanup := group.Group("", func(c *gin.Context) {
		principal, ok := auth.PrincipalFromGin(c)
		if !ok {
			h.writeError(c, 401, "AUTH_REQUIRED", "请先登录")
			c.Abort()
			return
		}
		if !auth.IsInteractiveAuthType(principal.AuthType) || (!principal.HasRole(domain.RoleSuperAdmin) && !principal.HasRole(domain.RoleTenantAdmin)) {
			h.writeError(c, 403, "FORBIDDEN", "仅管理员登录会话可删除失败记录")
			c.Abort()
			return
		}
		if allowed, _ := limiter.Allow(principal.TenantID+"/"+principal.Subject, sourceArtifactActionCreate); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "清理操作过于频繁，请稍后重试")
			c.Abort()
			return
		}
		c.Next()
	})
	cleanup.DELETE("/datasets/:id/versions/:versionID", func(c *gin.Context) { h.deleteFailedDatasetRecord(c, false) })
	cleanup.DELETE("/datasets/:id/versions/:versionID/publication", func(c *gin.Context) { h.deleteFailedDatasetRecord(c, true) })
	cleanup.DELETE("/datasets/:id/versions/:versionID/purge", h.purgeFailedDatasetVersion)
}
func (h *Handler) deleteFailedDatasetRecord(c *gin.Context, publicationOnly bool) {
	datasetID, versionID := c.Param("id"), c.Param("versionID")
	if !cleanupDatasetID.MatchString(datasetID) || domain.ValidateResolvedDatasetVersionID(versionID) != nil {
		h.writeError(c, 400, "INVALID_DATASET_ID", "数据集或版本 ID 不正确")
		return
	}
	store, ok := h.datasets.(DatasetCleanupStore)
	if !ok {
		h.writeError(c, 503, "DATASET_CLEANUP_UNAVAILABLE", "失败记录清理暂不可用")
		return
	}
	principal, _ := auth.PrincipalFromGin(c)
	err := store.DeleteFailedDatasetRecord(c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin), datasetID, versionID, principal.Subject, publicationOnly)
	switch {
	case errors.Is(err, repositories.ErrDatasetCleanupNotFound):
		h.writeError(c, 404, "DATASET_RECORD_NOT_FOUND", "记录不存在、已删除或不属于可管理范围")
	case errors.Is(err, repositories.ErrDatasetCleanupConflict):
		h.writeError(c, 409, "DATASET_CLEANUP_CONFLICT", "仅可删除失败且未被训练引用、无活动发布工作的记录，请刷新后重试")
	case err != nil:
		h.writeError(c, 500, "DATASET_CLEANUP_FAILED", "清理失败，请稍后重试")
	default:
		h.writeSuccess(c, 200, gin.H{"deleted": true})
	}
}
