package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// ImageStore is the catalogue of runtime environments users can pick from.
type ImageStore interface {
	CreateImage(ctx context.Context, image domain.PlatformImage) error
	ListImages(ctx context.Context, tenantID, kind string) ([]domain.PlatformImage, error)
	DefaultImage(ctx context.Context, tenantID, kind string) (domain.PlatformImage, error)
	ImageByReference(ctx context.Context, tenantID, kind, reference string) (domain.PlatformImage, error)
	SetImageShared(ctx context.Context, tenantID, id string, shared bool, targetTenantID string) (domain.PlatformImage, error)
	DeleteImage(ctx context.Context, tenantID, id string, superAdmin bool) error
}

func (h *Handler) RegisterImageRoutes(group *gin.RouterGroup) {
	h.RegisterImageReadRoutes(group)
	h.RegisterImageManagementRoutes(group)
}

// RegisterImageReadRoutes stays on the authenticated API group because CLI
// machine tokens must resolve and validate an administrator-approved runtime
// before submitting a job.
func (h *Handler) RegisterImageReadRoutes(group *gin.RouterGroup) {
	read := group.Group("")
	read.Use(auth.RequireScopes(domain.PATScopeJobsWrite))
	read.GET("/images", h.listImages)
}

// RegisterImageManagementRoutes is mounted behind the interactive-session
// guard. Publishing or changing catalogue entries remains a human admin action.
func (h *Handler) RegisterImageManagementRoutes(group *gin.RouterGroup) {
	group.POST("/images", h.createImage)
	group.PATCH("/images/:id", h.updateImageScope)
	group.DELETE("/images/:id", h.deleteImage)
}

type updateImageScopeRequest struct {
	Shared         *bool  `json:"shared"`
	TargetTenantID string `json:"targetTenantId"`
}

func (h *Handler) updateImageScope(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	// Changing the visibility boundary affects every tenant, including moving a
	// previously shared image back into one team, so it is always SuperAdmin-only.
	if !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "super administrator role is required to change image scope")
		return
	}
	if h.images == nil {
		h.writeError(c, http.StatusServiceUnavailable, "IMAGE_CATALOG_UNAVAILABLE", "image catalog is not configured")
		return
	}
	var request updateImageScopeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	if request.Shared == nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_IMAGE_SCOPE", "shared is required")
		return
	}
	targetTenantID := ""
	if !*request.Shared {
		targetTenantID = strings.TrimSpace(request.TargetTenantID)
		if targetTenantID == "" || targetTenantID != principal.TenantID {
			h.writeError(c, http.StatusBadRequest, "INVALID_IMAGE_SCOPE", "targetTenantId must be your current tenant when removing platform access")
			return
		}
	}
	image, err := h.images.SetImageShared(c.Request.Context(), principal.TenantID, c.Param("id"), *request.Shared, targetTenantID)
	if errors.Is(err, repositories.ErrImageNotFound) {
		h.writeError(c, http.StatusNotFound, "IMAGE_NOT_FOUND", "image was not found in your catalog")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "IMAGE_SCOPE_UPDATE_FAILED", "could not change image scope")
		return
	}
	h.writeSuccess(c, http.StatusOK, image)
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
	Name             string                   `json:"name"`
	Reference        string                   `json:"reference"`
	Kind             string                   `json:"kind"`
	Description      string                   `json:"description"`
	Framework        string                   `json:"framework"`
	IsDefault        bool                     `json:"isDefault"`
	Shared           bool                     `json:"shared"`
	RayVersion       *string                  `json:"rayVersion"`
	SupportedEngines *[]domain.TrainingEngine `json:"supportedEngines"`
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
	rayVersion := domain.RayVersionLegacy
	if request.RayVersion != nil {
		rayVersion = *request.RayVersion
	}
	supportedEngines := []domain.TrainingEngine{domain.TrainingEngineRayDDP}
	if request.SupportedEngines != nil {
		supportedEngines = append([]domain.TrainingEngine(nil), (*request.SupportedEngines)...)
	}
	image := domain.PlatformImage{
		TenantID: tenantID, Name: request.Name,
		Reference: domain.NormalizeImageReference(request.Reference),
		Kind:      request.Kind, Description: request.Description, Framework: request.Framework,
		IsDefault: request.IsDefault, CreatedBy: principal.Subject,
		RayVersion:       rayVersion,
		SupportedEngines: supportedEngines,
	}
	if err := image.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_IMAGE", err.Error())
		return
	}
	id, err := h.newID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate image id")
		return
	}
	image.ID = id
	responseImage := image
	responseImage.SupportedEngines = append([]domain.TrainingEngine(nil), image.SupportedEngines...)
	persistedImage := image
	persistedImage.SupportedEngines = append([]domain.TrainingEngine(nil), image.SupportedEngines...)
	if err := h.images.CreateImage(c.Request.Context(), persistedImage); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_IMAGE", err.Error())
		return
	}
	h.writeSuccess(c, http.StatusCreated, responseImage)
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
