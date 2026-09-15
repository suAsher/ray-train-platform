package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	fw "ray-train-platform-backend/functionwarehouse"
	ws "ray-train-platform-backend/warehousesync"
)

func (h *Handler) InitializeFunctionWarehouseSync(ctx context.Context, store ws.Store, pepper []byte) error {
	clients := make(map[fw.Environment]ws.Upstream)
	for env, client := range h.functionWarehouses {
		if upstream, ok := client.(ws.Upstream); ok {
			clients[env] = upstream
		}
	}
	service, err := ws.NewService(store, warehouseSyncSource{h: h}, clients, pepper)
	if err != nil {
		return err
	}
	h.warehouseSync = service
	go service.Run(ctx)
	return nil
}

func (h *Handler) warehouseSyncActor(c *gin.Context) (ws.Actor, string, bool) {
	if h.warehouseSync == nil {
		h.writeError(c, 503, "WAREHOUSE_SYNC_UNAVAILABLE", "功能仓同步暂不可用")
		return ws.Actor{}, "", false
	}
	token, ok := h.functionWarehouseToken(c)
	if !ok {
		return ws.Actor{}, "", false
	}
	p, ok := auth.PrincipalFromGin(c)
	if !ok {
		return ws.Actor{}, "", false
	}
	return ws.Actor{ID: p.Subject, Name: p.Username, TenantID: p.TenantID}, token, true
}

func (h *Handler) createWarehouseSync(c *gin.Context) {
	a, token, ok := h.warehouseSyncActor(c)
	if !ok {
		return
	}
	var input ws.Request
	if !h.decodeModelJSON(c, &input) {
		return
	}
	key, ok := h.modelRequestKey(c)
	if !ok {
		return
	}
	input.IdempotencyKey = key
	p, _ := auth.PrincipalFromGin(c)
	job, err := h.jobForPrincipal(c.Request.Context(), p, input.JobID)
	if err != nil || job == nil || job.UserID != p.Subject || job.TenantID != p.TenantID {
		h.writeError(c, 403, "WAREHOUSE_SOURCE_FORBIDDEN", "只能同步本人当前团队训练任务中的文件")
		return
	}
	if !input.Automatic {
		switch job.ObservedState {
		case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
		default:
			h.writeError(c, 409, "WAREHOUSE_SOURCE_ACTIVE", "训练尚未结束，请选择训练成功后自动同步")
			return
		}
	}
	if _, ok := h.jobArtifactRoot(c, p, job); !ok {
		return
	}
	op, err := h.warehouseSync.Create(c.Request.Context(), a, input, token)
	if h.warehouseSyncError(c, err) {
		return
	}
	h.writeSuccess(c, http.StatusAccepted, op)
}

func (h *Handler) listWarehouseSyncs(c *gin.Context) {
	a, _, ok := h.warehouseSyncActor(c)
	if !ok {
		return
	}
	jobID := c.Query("jobId")
	if jobID == "" || len(jobID) > 128 {
		h.writeError(c, 400, "WAREHOUSE_JOB_REQUIRED", "请选择训练任务")
		return
	}
	items, err := h.warehouseSync.ListJob(c.Request.Context(), a, jobID)
	if h.warehouseSyncError(c, err) {
		return
	}
	h.writeSuccess(c, http.StatusOK, gin.H{"items": items})
}

func (h *Handler) retryWarehouseSync(c *gin.Context) {
	a, token, ok := h.warehouseSyncActor(c)
	if !ok {
		return
	}
	op, err := h.warehouseSync.Retry(c.Request.Context(), c.Param("syncId"), a, token)
	if h.warehouseSyncError(c, err) {
		return
	}
	h.writeSuccess(c, http.StatusAccepted, op)
}

func (h *Handler) cancelWarehouseSync(c *gin.Context) {
	a, _, ok := h.warehouseSyncActor(c)
	if !ok {
		return
	}
	op, err := h.warehouseSync.Cancel(c.Request.Context(), c.Param("syncId"), a)
	if h.warehouseSyncError(c, err) {
		return
	}
	h.writeSuccess(c, http.StatusOK, op)
}

func (h *Handler) warehouseSyncError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ws.ErrInvalid):
		h.writeError(c, 400, "WAREHOUSE_SYNC_INVALID", "请检查目标、版本名及权重相对路径；每次最多选择8个文件")
	case errors.Is(err, ws.ErrForbidden):
		h.writeError(c, 403, "WAREHOUSE_SYNC_FORBIDDEN", "没有此同步操作或来源文件的管理权限")
	case errors.Is(err, ws.ErrNotFound):
		h.writeError(c, 404, "WAREHOUSE_SYNC_NOT_FOUND", "同步记录不存在")
	case errors.Is(err, ws.ErrConflict):
		h.writeError(c, 409, "WAREHOUSE_SYNC_CONFLICT", "操作状态已变化；登记结果待确认时，请核对功能仓后再发起新的同步")
	case errors.Is(err, ws.ErrQuota):
		h.writeError(c, 429, "WAREHOUSE_SYNC_QUOTA", "待处理同步过多，请等待已有操作结束")
	default:
		return h.functionWarehouseError(c, err)
	}
	return true
}
