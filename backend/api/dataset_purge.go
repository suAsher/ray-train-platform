package api

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
	"time"
)

type DatasetPurgeObjects func(context.Context, repositories.DatasetPurgePlan) (int, error)
type datasetPurgeRepository interface {
	PurgeFailedDatasetRecord(context.Context, string, bool, string, string, string, func(repositories.DatasetPurgePlan) (int, error)) (int, error)
}

// ConfigureDatasetPurge must be called only during startup, before serving.
// An absent storage dependency fails closed instead of merely hiding records.
func (h *Handler) ConfigureDatasetPurge(purge DatasetPurgeObjects) { h.datasetPurgeObjects = purge }

func (h *Handler) purgeFailedDatasetVersion(c *gin.Context) {
	datasetID, versionID := c.Param("id"), c.Param("versionID")
	if !cleanupDatasetID.MatchString(datasetID) || domain.ValidateResolvedDatasetVersionID(versionID) != nil {
		h.writeError(c, 400, "INVALID_DATASET_ID", "数据集或版本 ID 不正确")
		return
	}
	store, ok := h.datasets.(datasetPurgeRepository)
	if !ok || h.datasetPurgeObjects == nil {
		h.writeError(c, 503, "DATASET_PURGE_UNAVAILABLE", "永久清理未配置，未删除记录或文件")
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	deleted, err := store.PurgeFailedDatasetRecord(ctx, p.TenantID, p.HasRole(domain.RoleSuperAdmin), datasetID, versionID, p.Subject, func(plan repositories.DatasetPurgePlan) (int, error) { return h.datasetPurgeObjects(ctx, plan) })
	switch {
	case errors.Is(err, repositories.ErrDatasetCleanupNotFound):
		h.writeError(c, 404, "DATASET_RECORD_NOT_FOUND", "失败版本不存在或不属于可管理范围")
	case errors.Is(err, repositories.ErrDatasetCleanupConflict):
		h.writeError(c, 409, "DATASET_CLEANUP_CONFLICT", "版本存在引用、活动发布工作或无法确认独占的产物，未完成清理")
	case err != nil:
		h.writeError(c, 503, "DATASET_PURGE_INCOMPLETE", "清理未完成，记录保留供重试；部分独占文件可能已删除。原始和共享文件未删除")
	default:
		h.writeSuccess(c, 200, gin.H{"deleted": true, "exclusiveObjectsDeleted": deleted, "sharedObjectsRetained": true})
	}
}
