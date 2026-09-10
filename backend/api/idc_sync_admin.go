package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

type IDCDataSyncManager interface {
	CreateConnector(context.Context, domain.IDCDataSyncConnector) error
	ListConnectors(context.Context) ([]domain.IDCDataSyncConnector, error)
	ListRuns(context.Context, string) ([]domain.IDCDataSyncRun, error)
	Request(context.Context, domain.IDCDataSyncConnector, string) (domain.IDCDataSyncRun, error)
	UpdateConnector(context.Context, domain.IDCDataSyncConnector) (domain.IDCDataSyncConnector, error)
}

type createIDCDataSyncConnectorRequest struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	SourceRelativePath  string `json:"sourceRelativePath"`
	MirrorPrefix        string `json:"mirrorPrefix"`
	SyncIntervalMinutes int    `json:"syncIntervalMinutes"`
}

type updateIDCDataSyncConnectorRequest struct {
	Enabled             *bool `json:"enabled"`
	SyncIntervalMinutes *int  `json:"syncIntervalMinutes"`
}

// RegisterIDCSyncManagementRoutes adds backend-only administrative endpoints.
// The deferred frontend migration may consume these later; no browser change
// is required for the data-plane implementation.
func (h *Handler) RegisterIDCSyncManagementRoutes(group *gin.RouterGroup) {
	group.GET("/admin/idc-sync/connectors", h.listIDCDataSyncConnectors)
	group.GET("/admin/idc-sync/connectors/:id/runs", h.listIDCDataSyncRuns)
	group.POST("/admin/idc-sync/connectors", h.createIDCDataSyncConnector)
	group.PATCH("/admin/idc-sync/connectors/:id", h.updateIDCDataSyncConnector)
	group.POST("/admin/idc-sync/connectors/:id/runs", h.requestIDCDataSyncRun)
}

func (h *Handler) listIDCDataSyncRuns(c *gin.Context) {
	if _, ok := h.requireIDCDataSyncAdmin(c); !ok {
		return
	}
	items, err := h.idcSyncManager.ListRuns(c.Request.Context(), strings.TrimSpace(c.Param("id")))
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IDC_SYNC_LIST_FAILED", "could not list IDC sync runs")
		return
	}
	h.writeSuccess(c, http.StatusOK, items)
}

func (h *Handler) requireIDCDataSyncAdmin(c *gin.Context) (string, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return "", false
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "super administrator role is required")
		return "", false
	}
	if h.idcSyncManager == nil {
		h.writeError(c, http.StatusServiceUnavailable, "IDC_SYNC_UNAVAILABLE", "IDC sync is not configured")
		return "", false
	}
	return principal.Subject, true
}

func (h *Handler) listIDCDataSyncConnectors(c *gin.Context) {
	if _, ok := h.requireIDCDataSyncAdmin(c); !ok {
		return
	}
	items, err := h.idcSyncManager.ListConnectors(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IDC_SYNC_LIST_FAILED", "could not list IDC sync connectors")
		return
	}
	h.writeSuccess(c, http.StatusOK, items)
}

func (h *Handler) createIDCDataSyncConnector(c *gin.Context) {
	requestedBy, ok := h.requireIDCDataSyncAdmin(c)
	if !ok {
		return
	}
	var request createIDCDataSyncConnectorRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	connector := domain.IDCDataSyncConnector{ID: strings.TrimSpace(request.ID), Name: strings.TrimSpace(request.Name), SourceSpace: domain.DataSpaceIDCOriginal, SourceRelativePath: strings.TrimSpace(request.SourceRelativePath), MirrorPrefix: strings.TrimSpace(request.MirrorPrefix), Enabled: true, SyncIntervalMinutes: request.SyncIntervalMinutes, CreatedBy: requestedBy}
	if err := h.idcSyncManager.CreateConnector(c.Request.Context(), connector); err != nil {
		h.writeError(c, http.StatusBadRequest, "IDC_SYNC_CONNECTOR_INVALID", "IDC sync connector is invalid or already exists")
		return
	}
	h.writeSuccess(c, http.StatusCreated, connector)
}

func (h *Handler) updateIDCDataSyncConnector(c *gin.Context) {
	if _, ok := h.requireIDCDataSyncAdmin(c); !ok {
		return
	}
	var request updateIDCDataSyncConnectorRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil && request.SyncIntervalMinutes == nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "connector policy is invalid")
		return
	}
	connectors, err := h.idcSyncManager.ListConnectors(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IDC_SYNC_LIST_FAILED", "could not look up IDC sync connector")
		return
	}
	for _, connector := range connectors {
		if connector.ID != c.Param("id") {
			continue
		}
		if request.Enabled != nil {
			connector.Enabled = *request.Enabled
		}
		if request.SyncIntervalMinutes != nil {
			connector.SyncIntervalMinutes = *request.SyncIntervalMinutes
		}
		updated, err := h.idcSyncManager.UpdateConnector(c.Request.Context(), connector)
		if err != nil {
			h.writeError(c, http.StatusBadRequest, "IDC_SYNC_CONNECTOR_INVALID", "IDC sync connector policy is invalid")
			return
		}
		h.writeSuccess(c, http.StatusOK, updated)
		return
	}
	h.writeError(c, http.StatusNotFound, "IDC_SYNC_CONNECTOR_NOT_FOUND", "IDC sync connector was not found")
}

func (h *Handler) requestIDCDataSyncRun(c *gin.Context) {
	requestedBy, ok := h.requireIDCDataSyncAdmin(c)
	if !ok {
		return
	}
	connectors, err := h.idcSyncManager.ListConnectors(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IDC_SYNC_LIST_FAILED", "could not look up IDC sync connector")
		return
	}
	for _, connector := range connectors {
		if connector.ID != c.Param("id") {
			continue
		}
		run, err := h.idcSyncManager.Request(c.Request.Context(), connector, requestedBy)
		if err != nil {
			h.writeError(c, http.StatusConflict, "IDC_SYNC_START_FAILED", "IDC sync could not be started")
			return
		}
		h.writeSuccess(c, http.StatusAccepted, run)
		return
	}
	h.writeError(c, http.StatusNotFound, "IDC_SYNC_CONNECTOR_NOT_FOUND", "IDC sync connector was not found")
}
