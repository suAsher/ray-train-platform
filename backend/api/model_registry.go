package api

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelregistry"
	"time"
)

func (h *Handler) RegisterModelRegistryRoutes(group *gin.RouterGroup) {
	read := group.Group("", auth.RequireInteractiveSession(false), h.modelGuard(false), h.modelRegistryGuard())
	read.GET("/models/:modelId/versions/:versionId/registry", h.getModelRegistry)
	write := group.Group("", auth.RequireInteractiveSession(false), h.modelGuard(true), h.modelRegistryGuard())
	write.POST("/models/:modelId/versions/:versionId/registry", h.syncModelRegistry)
}
func (h *Handler) modelRegistryGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !validLifecycleRoute(c) {
			h.modelError(c, ml.ErrInvalid)
			c.Abort()
			return
		}
		if h.modelRegistryLinks == nil {
			h.writeError(c, 503, "MODEL_REGISTRY_UNAVAILABLE", "MLflow 模型关联服务暂不可用")
			c.Abort()
			return
		}
		c.Next()
	}
}
func (h *Handler) getModelRegistry(c *gin.Context) {
	m, ok := h.modelForRequest(c, false)
	if !ok {
		return
	}
	_, err := h.models.GetVersion(c.Request.Context(), m.ID, c.Param("versionId"))
	if h.modelError(c, err) {
		return
	}
	r, err := h.modelRegistryLinks.Get(c.Request.Context(), c.Param("versionId"))
	if h.registryError(c, err) {
		return
	}
	h.writeSuccess(c, 200, r)
}
func (h *Handler) syncModelRegistry(c *gin.Context) {
	m, ok := h.modelForRequest(c, true)
	if !ok {
		return
	}
	if m.Archived {
		h.modelError(c, ml.ErrConflict)
		return
	}
	if h.modelRegistry == nil || h.modelSnapshots == nil {
		h.writeError(c, 503, "MODEL_REGISTRY_UNAVAILABLE", "MLflow 模型关联服务暂不可用")
		return
	}
	var body struct{}
	if !h.decodeModelJSON(c, &body) {
		return
	}
	v, err := h.models.GetVersion(c.Request.Context(), m.ID, c.Param("versionId"))
	if h.modelError(c, err) {
		return
	}
	if v.State != ml.Ready {
		h.modelError(c, ml.ErrNotReady)
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	r, acquired, err := h.modelRegistryLinks.Acquire(c.Request.Context(), m.ID, v.ID, p.Subject, p.HasRole(domain.RoleSuperAdmin))
	if h.registryError(c, err) {
		return
	}
	if acquired {
		go h.runModelRegistry(context.WithoutCancel(c.Request.Context()), m, v, r.LeaseID)
	}
	code := 202
	if r.State == "READY" {
		code = 200
	}
	h.writeSuccess(c, code, r)
}
func (h *Handler) registryError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mr.ErrConflict) {
		h.writeError(c, 409, "MODEL_REGISTRY_CONFLICT", "模型关联正在处理或记录已变化，请稍后刷新")
		return true
	}
	if errors.Is(err, ml.ErrConflict) || errors.Is(err, ml.ErrNotFound) || errors.Is(err, ml.ErrNotReady) {
		return h.modelError(c, err)
	}
	h.writeError(c, 503, "MODEL_REGISTRY_FAILED", "MLflow 模型关联暂不可用，请稍后重试")
	return true
}

// snapshotReader couples cancellation to both pipe ends and the storage fetch.
// Provider.Close releases the writer even if it stops reading before EOF.
func snapshotReader(ctx context.Context, service modelSnapshotService, v ml.Version) (io.ReadCloser, error) {
	child, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	stop := context.AfterFunc(child, func() { _ = reader.CloseWithError(child.Err()); _ = writer.CloseWithError(child.Err()) })
	go func() { err := service.Download(child, v, writer); _ = writer.CloseWithError(err); stop(); cancel() }()
	return &registrySnapshotReader{PipeReader: reader, cancel: cancel}, nil
}

type registrySnapshotReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *registrySnapshotReader) Close() error { r.cancel(); return r.PipeReader.Close() }
func (h *Handler) runModelRegistry(parent context.Context, m ml.Model, v ml.Version, lease string) {
	ctx, cancel := context.WithTimeout(parent, mr.WorkerTimeout)
	defer cancel()
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewalCtx, done := context.WithTimeout(ctx, 15*time.Second)
				err := h.modelRegistryLinks.Renew(renewalCtx, v.ID, lease)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	link, err := h.modelRegistry.EnsureVersion(ctx, m, v, func(ctx context.Context) (io.ReadCloser, error) { return snapshotReader(ctx, h.modelSnapshots, v) })
	// A cancelled request may still be completing upstream. Keep its lease until
	// the conservative expiry rather than immediately allowing another worker.
	wasCancelled := ctx.Err() != nil
	cancel()
	<-renewed
	if wasCancelled {
		return
	}
	failure := ""
	if err != nil {
		failure = "MLflow 关联失败，请稍后重试；现有模型与权重未受影响"
	}
	finish, done := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer done()
	_ = h.modelRegistryLinks.Complete(finish, v.ID, lease, link, failure)
}
