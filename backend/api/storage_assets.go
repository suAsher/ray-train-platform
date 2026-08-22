package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// StorageAssetStore is the small catalogue interface needed by both the Portal
// and submission service. It exposes scope-aware reads so callers cannot make
// an unscoped lookup and accidentally learn another tenant's storage roots.
type StorageAssetStore interface {
	CreateStorageAsset(context.Context, domain.StorageAsset) error
	ListStorageAssets(context.Context, string, string, string) ([]domain.StorageAsset, error)
	GetStorageAsset(context.Context, string, string, string) (domain.StorageAsset, error)
	DeleteStorageAsset(context.Context, string, string, bool) error
}

// storageAssetResponse intentionally omits claimName and rootPrefix. Those
// infrastructure details are server-side authorization inputs, never Portal
// data, even for users who are allowed to choose the asset.
type storageAssetResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Kind          string `json:"kind"`
	Provider      string `json:"provider"`
	ReadOnly      bool   `json:"readOnly"`
	BrowseEnabled bool   `json:"browseEnabled"`
}

func storageAssetForResponse(asset domain.StorageAsset) storageAssetResponse {
	return storageAssetResponse{
		ID: asset.ID, Name: asset.Name, Description: asset.Description, Kind: asset.Kind,
		Provider: asset.Provider, ReadOnly: asset.ReadOnly, BrowseEnabled: asset.BrowseEnabled,
	}
}

func (h *Handler) RegisterStorageAssetRoutes(group *gin.RouterGroup) {
	group.GET("/storage-assets", h.listStorageAssets)
	group.GET("/storage-assets/:id/directories", h.listStorageAssetDirectories)
	group.POST("/storage-assets", h.createStorageAsset)
	group.DELETE("/storage-assets/:id", h.deleteStorageAsset)
}

func (h *Handler) listStorageAssets(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.storageAssets == nil {
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_CATALOG_UNAVAILABLE", "storage catalogue is not configured")
		return
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind != "" {
		if err := domain.ValidateStorageAssetKind(kind); err != nil {
			h.writeError(c, http.StatusBadRequest, "INVALID_STORAGE_ASSET_KIND", err.Error())
			return
		}
	}
	assets, err := h.storageAssets.ListStorageAssets(c.Request.Context(), principal.TenantID, principal.Subject, kind)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "STORAGE_CATALOG_LIST_FAILED", "could not list storage assets")
		return
	}
	response := make([]storageAssetResponse, 0, len(assets))
	for _, asset := range assets {
		response = append(response, storageAssetForResponse(asset))
	}
	h.writeSuccess(c, http.StatusOK, response)
}

func (h *Handler) listStorageAssetDirectories(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.storageAssets == nil {
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_CATALOG_UNAVAILABLE", "storage catalogue is not configured")
		return
	}
	asset, err := h.storageAssets.GetStorageAsset(c.Request.Context(), principal.TenantID, principal.Subject, c.Param("id"))
	if errors.Is(err, repositories.ErrStorageAssetNotFound) {
		h.writeError(c, http.StatusNotFound, "STORAGE_ASSET_NOT_FOUND", "storage asset was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "STORAGE_ASSET_LOOKUP_FAILED", "could not look up storage asset")
		return
	}
	if asset.Provider != domain.StorageProviderTOS || !asset.BrowseEnabled {
		h.writeError(c, http.StatusConflict, "STORAGE_BROWSER_NOT_AVAILABLE", "directory browsing is not available for this storage asset")
		return
	}
	path, err := domain.NormalizeStorageRelativePath(c.Query("path"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_STORAGE_PATH", "directory path must stay inside the selected storage root")
		return
	}
	if h.directoryLister == nil {
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_BROWSER_UNAVAILABLE", "storage directory browser is not configured")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 0 {
		h.writeError(c, http.StatusBadRequest, "INVALID_STORAGE_PAGE_LIMIT", "directory page limit is invalid")
		return
	}
	page, err := h.directoryLister.ListDirectories(c.Request.Context(), asset.RootPrefix, path, c.Query("cursor"), limit)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_BROWSER_UNAVAILABLE", "could not browse storage directories")
		return
	}
	h.writeSuccess(c, http.StatusOK, page)
}

type createStorageAssetRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Kind          string `json:"kind"`
	Provider      string `json:"provider"`
	ClaimName     string `json:"claimName"`
	RootPrefix    string `json:"rootPrefix"`
	ReadOnly      bool   `json:"readOnly"`
	BrowseEnabled bool   `json:"browseEnabled"`
	Shared        bool   `json:"shared"`
}

func (h *Handler) createStorageAsset(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if h.storageAssets == nil {
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_CATALOG_UNAVAILABLE", "storage catalogue is not configured")
		return
	}
	var request createStorageAssetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	tenantID := principal.TenantID
	if request.Shared {
		if !principal.HasRole(domain.RoleSuperAdmin) {
			h.writeError(c, http.StatusForbidden, "SHARED_STORAGE_ASSET_FORBIDDEN", "only a super administrator can publish a shared storage asset")
			return
		}
		tenantID = ""
	}
	id, err := h.newID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate storage asset id")
		return
	}
	asset := domain.StorageAsset{
		ID: id, TenantID: tenantID, Name: request.Name, Description: request.Description,
		Kind: request.Kind, Provider: request.Provider, ClaimName: request.ClaimName,
		RootPrefix: request.RootPrefix, ReadOnly: request.ReadOnly, BrowseEnabled: request.BrowseEnabled,
		CreatedBy: principal.Subject,
	}
	if err := h.storageAssets.CreateStorageAsset(c.Request.Context(), asset); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_STORAGE_ASSET", "storage asset configuration is invalid")
		return
	}
	h.writeSuccess(c, http.StatusCreated, storageAssetForResponse(asset))
}

func (h *Handler) deleteStorageAsset(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if h.storageAssets == nil {
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_CATALOG_UNAVAILABLE", "storage catalogue is not configured")
		return
	}
	err := h.storageAssets.DeleteStorageAsset(c.Request.Context(), principal.TenantID, c.Param("id"), principal.HasRole(domain.RoleSuperAdmin))
	if errors.Is(err, repositories.ErrStorageAssetNotFound) {
		h.writeError(c, http.StatusNotFound, "STORAGE_ASSET_NOT_FOUND", "storage asset was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "STORAGE_ASSET_DELETE_FAILED", "could not delete storage asset")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]bool{"deleted": true})
}
