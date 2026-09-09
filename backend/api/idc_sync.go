package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// IDCDataSyncCallbackStore is intentionally narrower than a general dataset
// store. A worker may append its own immutable inventory and seal that exact
// run; it cannot create connectors, select a source, or alter another run.
type IDCDataSyncCallbackStore interface {
	AppendIDCDataSyncInventory(context.Context, string, []domain.IDCDataSyncInventoryEntry) error
	CompleteIDCDataSyncRun(context.Context, string, string, string) (domain.IDCDataSyncRun, error)
}

type idcSyncEntriesRequest struct {
	RunID   string                             `json:"runId"`
	Entries []domain.IDCDataSyncInventoryEntry `json:"entries"`
}

type idcSyncCompleteRequest struct {
	RunID              string `json:"runId"`
	InventorySHA256    string `json:"inventorySha256"`
	InventoryObjectKey string `json:"inventoryObjectKey"`
}

// RegisterIDCSyncInternalRoutes deliberately lives outside end-user auth.
// Calls are guarded by a deterministic run-scoped HMAC derived from the
// platform secret, and the route accepts no user-controlled destination.
func (h *Handler) RegisterIDCSyncInternalRoutes(group *gin.RouterGroup) {
	if h == nil || h.idcSyncCallbacks == nil || len(h.idcSyncCallbackKey) == 0 {
		return
	}
	group.POST("/idc-sync/runs/:id/entries", h.appendIDCDataSyncEntries)
	group.POST("/idc-sync/runs/:id/complete", h.completeIDCDataSyncRun)
}

func (h *Handler) appendIDCDataSyncEntries(c *gin.Context) {
	if !h.authorizeIDCDataSyncCallback(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20)
	var request idcSyncEntriesRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.RunID != c.Param("id") || len(request.Entries) == 0 || len(request.Entries) > 1000 {
		h.writeError(c, http.StatusBadRequest, "IDC_SYNC_RECEIPT_INVALID", "sync receipt is invalid")
		return
	}
	if err := h.idcSyncCallbacks.AppendIDCDataSyncInventory(c.Request.Context(), request.RunID, request.Entries); err != nil {
		h.writeIDCDataSyncCallbackError(c, err)
		return
	}
	h.writeSuccess(c, http.StatusNoContent, nil)
}

func (h *Handler) completeIDCDataSyncRun(c *gin.Context) {
	if !h.authorizeIDCDataSyncCallback(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var request idcSyncCompleteRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.RunID != c.Param("id") {
		h.writeError(c, http.StatusBadRequest, "IDC_SYNC_RECEIPT_INVALID", "sync receipt is invalid")
		return
	}
	if _, err := h.idcSyncCallbacks.CompleteIDCDataSyncRun(c.Request.Context(), request.RunID, request.InventorySHA256, request.InventoryObjectKey); err != nil {
		h.writeIDCDataSyncCallbackError(c, err)
		return
	}
	h.writeSuccess(c, http.StatusNoContent, nil)
}

func (h *Handler) authorizeIDCDataSyncCallback(c *gin.Context) bool {
	runID := c.Param("id")
	provided := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	mac := hmac.New(sha256.New, h.idcSyncCallbackKey)
	_, _ = mac.Write([]byte("idc-sync:" + runID))
	want := hex.EncodeToString(mac.Sum(nil))
	if runID == "" || len(provided) != len(want) || !hmac.Equal([]byte(provided), []byte(want)) {
		h.writeError(c, http.StatusUnauthorized, "IDC_SYNC_CALLBACK_UNAUTHORIZED", "sync callback is unauthorized")
		return false
	}
	return true
}

func (h *Handler) writeIDCDataSyncCallbackError(c *gin.Context, err error) {
	if err == repositories.ErrIDCDataSyncRunNotFound {
		h.writeError(c, http.StatusNotFound, "IDC_SYNC_RUN_NOT_FOUND", "sync run was not found")
		return
	}
	h.writeError(c, http.StatusConflict, "IDC_SYNC_RECEIPT_REJECTED", "sync receipt was rejected")
}
