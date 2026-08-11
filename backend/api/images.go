package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// ImageStore is the catalogue of runtime environments users can pick from.
type ImageStore interface {
	CreateImage(ctx context.Context, image domain.PlatformImage) error
	ListImages(ctx context.Context, tenantID, kind string) ([]domain.PlatformImage, error)
	DefaultImage(ctx context.Context, tenantID, kind string) (domain.PlatformImage, error)
	ImageByReference(ctx context.Context, tenantID, kind, reference string) (domain.PlatformImage, error)
	DeleteImage(ctx context.Context, tenantID, id string, superAdmin bool) error
}

func (h *Handler) RegisterImageRoutes(group *gin.RouterGroup) {
	// Listing is open to any signed-in user: choosing an environment is part of
	// submitting a job. Publishing and removing are administrative.
	group.GET("/images", h.listImages)
	group.POST("/images", h.createImage)
	group.DELETE("/images/:id", h.deleteImage)
}

func (h *Handler) listImages(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.images == nil {
		h.writeError(c, http.StatusServiceUnavailable, "IMAGE_CATALOG_UNAVAILABLE", "image catalog is not configured")
		return
	}
	kind := c.Query("kind")
	if kind != "" {
		if err := domain.ValidateImageKind(kind); err != nil {
			h.writeError(c, http.StatusBadRequest, "INVALID_IMAGE_KIND", err.Error())
			return
		}
	}
	images, err := h.images.ListImages(c.Request.Context(), principal.TenantID, kind)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IMAGE_LIST_FAILED", "could not list images")
		return
	}
	h.writeSuccess(c, http.StatusOK, images)
}

type createImageRequest struct {
	Name        string `json:"name"`
	Reference   string `json:"reference"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Framework   string `json:"framework"`
	IsDefault   bool   `json:"isDefault"`
	Shared      bool   `json:"shared"`
}

func (h *Handler) createImage(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if h.images == nil {
		h.writeError(c, http.StatusServiceUnavailable, "IMAGE_CATALOG_UNAVAILABLE", "image catalog is not configured")
		return
	}
	var request createImageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	// Only a super administrator may publish into the catalogue every tenant
	// sees; a tenant admin's images stay inside their own tenant.
	tenantID := principal.TenantID
	if request.Shared {
		if !principal.HasRole(domain.RoleSuperAdmin) {
			h.writeError(c, http.StatusForbidden, "SHARED_IMAGE_FORBIDDEN", "only a super administrator can publish a shared image")
			return
		}
		tenantID = ""
	}
	id, err := h.newID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate image id")
		return
	}
	image := domain.PlatformImage{
		ID: id, TenantID: tenantID, Name: request.Name, Reference: request.Reference,
		Kind: request.Kind, Description: request.Description, Framework: request.Framework,
		IsDefault: request.IsDefault, CreatedBy: principal.Subject,
	}
	if err := h.images.CreateImage(c.Request.Context(), image); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_IMAGE", err.Error())
		return
	}
	h.writeSuccess(c, http.StatusCreated, image)
}

func (h *Handler) deleteImage(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if h.images == nil {
		h.writeError(c, http.StatusServiceUnavailable, "IMAGE_CATALOG_UNAVAILABLE", "image catalog is not configured")
		return
	}
	err := h.images.DeleteImage(c.Request.Context(), principal.TenantID, c.Param("id"), principal.HasRole(domain.RoleSuperAdmin))
	if errors.Is(err, repositories.ErrImageNotFound) {
		h.writeError(c, http.StatusNotFound, "IMAGE_NOT_FOUND", "image was not found in your catalog")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IMAGE_DELETE_FAILED", "could not delete image")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]bool{"deleted": true})
}
