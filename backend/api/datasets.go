package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

const internalDatasetObjectPrefix = "ray-train/platform/datasets"

type DatasetCatalogStore interface {
	CreateDataset(context.Context, domain.Dataset) error
	GetDataset(context.Context, string, bool, string) (domain.Dataset, error)
	ListDatasets(context.Context, string, bool) ([]domain.Dataset, error)
	GetDatasetVersion(context.Context, string, bool, string, string) (domain.DatasetVersion, error)
	ListDatasetVersions(context.Context, string, bool, string) ([]domain.DatasetVersion, error)
	ResolveReadyDatasetVersion(context.Context, string, bool, string, domain.DatasetVersionSelector) (domain.DatasetVersion, error)
	TransitionDatasetVersion(context.Context, string, string, domain.DatasetVersionState) (domain.DatasetVersion, error)
}

// DatasetPublicationManager is deliberately expressed only in logical domain
// objects. Its implementation owns TOS object keys, IRSA and Kubernetes
// identities; none of those values cross the user-facing API boundary.
type DatasetPublicationManager interface {
	RequestDatasetPublication(context.Context, domain.Dataset, string) (domain.DatasetPublicationRun, error)
	DryRunDatasetVersionGC(context.Context) ([]domain.DatasetVersion, error)
}

type datasetResponse struct {
	ID                 string                   `json:"id"`
	Slug               string                   `json:"slug"`
	Name               string                   `json:"name"`
	Description        string                   `json:"description,omitempty"`
	SourceSpace        domain.DataSpaceID       `json:"sourceSpace"`
	SourceRelativePath string                   `json:"sourceRelativePath"`
	OwnerTenantID      string                   `json:"ownerTenantId,omitempty"`
	Visibility         domain.DatasetVisibility `json:"visibility"`
	SchemaVersion      string                   `json:"schemaVersion"`
}

type datasetVersionResponse struct {
	ID                string                     `json:"id"`
	DatasetID         string                     `json:"datasetId"`
	Version           string                     `json:"version"`
	State             domain.DatasetVersionState `json:"state"`
	ManifestSHA256    string                     `json:"manifestSha256,omitempty"`
	SchemaVersion     string                     `json:"schemaVersion"`
	TrainSamples      int64                      `json:"trainSamples"`
	ValSamples        int64                      `json:"valSamples"`
	TestSamples       int64                      `json:"testSamples"`
	SourceObjectCount int64                      `json:"sourceObjectCount"`
	LogicalBytes      int64                      `json:"logicalBytes"`
	PackedBytes       int64                      `json:"packedBytes"`
}

type datasetPublicationResponse struct {
	ID                   string                     `json:"id"`
	DatasetID            string                     `json:"datasetId"`
	DatasetVersionID     string                     `json:"datasetVersionId"`
	State                domain.DatasetVersionState `json:"state"`
	TotalPartitions      int64                      `json:"totalPartitions"`
	CompletedPartitions  int64                      `json:"completedPartitions"`
	FailedPartitions     int64                      `json:"failedPartitions"`
	SourceObjectCount    int64                      `json:"sourceObjectCount"`
	ProcessedObjectCount int64                      `json:"processedObjectCount"`
	FailedObjectCount    int64                      `json:"failedObjectCount"`
}

type createDatasetRequest struct {
	Slug               string                   `json:"slug"`
	Name               string                   `json:"name"`
	Description        string                   `json:"description"`
	SourceRelativePath string                   `json:"sourceRelativePath"`
	Visibility         domain.DatasetVisibility `json:"visibility"`
	SchemaVersion      string                   `json:"schemaVersion"`
}

func (h *Handler) RegisterDatasetRoutes(group *gin.RouterGroup) {
	h.RegisterDatasetReadRoutes(group)
	h.RegisterDatasetManagementRoutes(group)
}

// RegisterDatasetReadRoutes is mounted on the normal authenticated API so
// browser sessions and PAT-authenticated spk-rayjob clients share one catalog.
func (h *Handler) RegisterDatasetReadRoutes(group *gin.RouterGroup) {
	read := group.Group("")
	read.Use(auth.RequireScopes(domain.PATScopeJobsRead))
	read.GET("/datasets", h.listDatasets)
	read.GET("/datasets/:id", h.getDataset)
	read.GET("/datasets/:id/versions", h.listDatasetVersions)
	read.GET("/datasets/:id/versions/latest", h.getLatestDatasetVersion)
	read.GET("/datasets/:id/versions/:versionID", h.getDatasetVersion)
}

// RegisterDatasetManagementRoutes requires the caller to have passed the
// platform's interactive-session guard in addition to the role checks below.
func (h *Handler) RegisterDatasetManagementRoutes(group *gin.RouterGroup) {
	group.POST("/datasets", h.createDataset)
	group.POST("/datasets/gc/dry-run", h.dryRunDatasetGC)
	group.POST("/datasets/:id/publications", h.requestDatasetPublication)
	group.POST("/datasets/:id/versions/:versionID/deprecate", h.deprecateDatasetVersion)
}

func (h *Handler) listDatasets(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok || !h.requireDatasetCatalog(c) {
		return
	}
	datasets, err := h.datasets.ListDatasets(c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin))
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DATASET_LIST_FAILED", "could not list datasets")
		return
	}
	response := make([]datasetResponse, 0, len(datasets))
	for _, dataset := range datasets {
		response = append(response, datasetForResponse(dataset))
	}
	h.writeSuccess(c, http.StatusOK, response)
}

func (h *Handler) getDataset(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok || !h.requireDatasetCatalog(c) {
		return
	}
	dataset, err := h.datasets.GetDataset(c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin), c.Param("id"))
	if h.writeDatasetLookupError(c, err) {
		return
	}
	h.writeSuccess(c, http.StatusOK, datasetForResponse(dataset))
}

func (h *Handler) listDatasetVersions(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok || !h.requireDatasetCatalog(c) {
		return
	}
	versions, err := h.datasets.ListDatasetVersions(c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin), c.Param("id"))
	if errors.Is(err, repositories.ErrDatasetNotFound) {
		h.writeError(c, http.StatusNotFound, "DATASET_NOT_FOUND", "dataset was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DATASET_VERSION_LIST_FAILED", "could not list dataset versions")
		return
	}
	response := make([]datasetVersionResponse, 0, len(versions))
	for _, version := range versions {
		response = append(response, datasetVersionForResponse(version))
	}
	h.writeSuccess(c, http.StatusOK, response)
}

func (h *Handler) getDatasetVersion(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok || !h.requireDatasetCatalog(c) {
		return
	}
	version, err := h.datasets.GetDatasetVersion(c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin), c.Param("id"), c.Param("versionID"))
	if errors.Is(err, repositories.ErrDatasetVersionNotFound) {
		h.writeError(c, http.StatusNotFound, "DATASET_VERSION_NOT_FOUND", "dataset version was not found")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DATASET_VERSION_LOOKUP_FAILED", "could not look up dataset version")
		return
	}
	h.writeSuccess(c, http.StatusOK, datasetVersionForResponse(version))
}

func (h *Handler) getLatestDatasetVersion(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok || !h.requireDatasetCatalog(c) {
		return
	}
	version, err := h.datasets.ResolveReadyDatasetVersion(
		c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin), c.Param("id"), domain.DatasetVersionSelector{Latest: true},
	)
	if errors.Is(err, repositories.ErrDatasetVersionNotReady) {
		h.writeError(c, http.StatusNotFound, "DATASET_READY_VERSION_NOT_FOUND", "dataset has no ready version")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DATASET_VERSION_LOOKUP_FAILED", "could not resolve the latest dataset version")
		return
	}
	h.writeSuccess(c, http.StatusOK, datasetVersionForResponse(version))
}

func (h *Handler) createDataset(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok {
		return
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return
	}
	if !h.requireDatasetCatalog(c) {
		return
	}
	var request createDatasetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	if isInternalDatasetSourcePath(request.SourceRelativePath, h.datasetInternalPrefix) {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATASET_SOURCE", "dataset source must stay inside the selected governed data space")
		return
	}

	sourceSpace := domain.DataSpaceTeamShared
	ownerTenantID := principal.TenantID
	switch request.Visibility {
	case domain.DatasetVisibilityPublic:
		if !principal.HasRole(domain.RoleSuperAdmin) {
			h.writeError(c, http.StatusForbidden, "PUBLIC_DATASET_FORBIDDEN", "only a platform administrator can publish a public dataset")
			return
		}
		sourceSpace, ownerTenantID = domain.DataSpacePublic, ""
	case domain.DatasetVisibilityTeam:
		if strings.TrimSpace(principal.TenantID) == "" {
			h.writeError(c, http.StatusForbidden, "TEAM_DATASET_FORBIDDEN", "a tenant is required for a team dataset")
			return
		}
	default:
		h.writeError(c, http.StatusBadRequest, "INVALID_DATASET_VISIBILITY", "dataset visibility must be PUBLIC or TEAM")
		return
	}

	id, err := newDatasetID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate dataset ID")
		return
	}
	dataset := domain.Dataset{
		ID: id, Slug: request.Slug, Name: request.Name, Description: request.Description,
		SourceSpace: sourceSpace, SourceRelativePath: request.SourceRelativePath,
		OwnerTenantID: ownerTenantID, Visibility: request.Visibility, SchemaVersion: request.SchemaVersion,
	}
	if err := dataset.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATASET", "dataset definition is invalid")
		return
	}
	if err := h.datasets.CreateDataset(c.Request.Context(), dataset); err != nil {
		if errors.Is(err, repositories.ErrDatasetConflict) {
			h.writeError(c, http.StatusConflict, "DATASET_CONFLICT", "dataset ID or slug already exists in this scope")
			return
		}
		h.writeError(c, http.StatusInternalServerError, "DATASET_CREATE_FAILED", "could not create dataset")
		return
	}
	h.writeSuccess(c, http.StatusCreated, datasetForResponse(dataset))
}

func (h *Handler) requestDatasetPublication(c *gin.Context) {
	principal, dataset, ok := h.manageableDataset(c)
	if !ok {
		return
	}
	if h.datasetPublications == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATASET_PUBLISHER_UNAVAILABLE", "dataset publisher is not configured")
		return
	}
	run, err := h.datasetPublications.RequestDatasetPublication(c.Request.Context(), dataset, principal.Subject)
	if err != nil || run.Validate() != nil || run.DatasetID != dataset.ID {
		h.writeError(c, http.StatusServiceUnavailable, "DATASET_PUBLICATION_FAILED", "could not request dataset publication")
		return
	}
	h.writeSuccess(c, http.StatusAccepted, datasetPublicationForResponse(run))
}

func (h *Handler) deprecateDatasetVersion(c *gin.Context) {
	_, dataset, ok := h.manageableDataset(c)
	if !ok {
		return
	}
	version, err := h.datasets.TransitionDatasetVersion(c.Request.Context(), dataset.ID, c.Param("versionID"), domain.DatasetVersionDeprecated)
	if errors.Is(err, repositories.ErrDatasetVersionNotFound) {
		h.writeError(c, http.StatusNotFound, "DATASET_VERSION_NOT_FOUND", "dataset version was not found")
		return
	}
	if errors.Is(err, repositories.ErrDatasetVersionConflict) {
		h.writeError(c, http.StatusConflict, "DATASET_VERSION_STATE_CONFLICT", "only a ready dataset version can be deprecated")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DATASET_VERSION_UPDATE_FAILED", "could not deprecate dataset version")
		return
	}
	h.writeSuccess(c, http.StatusOK, datasetVersionForResponse(version))
}

func (h *Handler) dryRunDatasetGC(c *gin.Context) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok {
		return
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "platform administrator role is required")
		return
	}
	if h.datasetPublications == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATASET_PUBLISHER_UNAVAILABLE", "dataset publisher is not configured")
		return
	}
	versions, err := h.datasetPublications.DryRunDatasetVersionGC(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATASET_GC_PREVIEW_FAILED", "could not preview dataset version cleanup")
		return
	}
	candidates := make([]datasetVersionResponse, 0, len(versions))
	for _, version := range versions {
		candidates = append(candidates, datasetVersionForResponse(version))
	}
	h.writeSuccess(c, http.StatusOK, gin.H{"count": len(candidates), "candidates": candidates})
}

func (h *Handler) manageableDataset(c *gin.Context) (auth.Principal, domain.Dataset, bool) {
	principal, ok := h.requireDatasetPrincipal(c)
	if !ok {
		return auth.Principal{}, domain.Dataset{}, false
	}
	if !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "tenant administrator role is required")
		return auth.Principal{}, domain.Dataset{}, false
	}
	if !h.requireDatasetCatalog(c) {
		return auth.Principal{}, domain.Dataset{}, false
	}
	dataset, err := h.datasets.GetDataset(c.Request.Context(), principal.TenantID, principal.HasRole(domain.RoleSuperAdmin), c.Param("id"))
	if h.writeDatasetLookupError(c, err) {
		return auth.Principal{}, domain.Dataset{}, false
	}
	if !domain.CanManageDataset(dataset, principal.TenantID, principal.HasRole(domain.RoleSuperAdmin)) {
		h.writeError(c, http.StatusForbidden, "DATASET_MANAGEMENT_FORBIDDEN", "dataset cannot be managed in the current tenant")
		return auth.Principal{}, domain.Dataset{}, false
	}
	return principal, dataset, true
}

func (h *Handler) requireDatasetPrincipal(c *gin.Context) (auth.Principal, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
	}
	return principal, ok
}

func (h *Handler) requireDatasetCatalog(c *gin.Context) bool {
	if h.datasets != nil {
		return true
	}
	h.writeError(c, http.StatusServiceUnavailable, "DATASET_CATALOG_UNAVAILABLE", "dataset catalogue is not configured")
	return false
}

func (h *Handler) writeDatasetLookupError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, repositories.ErrDatasetNotFound) {
		h.writeError(c, http.StatusNotFound, "DATASET_NOT_FOUND", "dataset was not found")
		return true
	}
	h.writeError(c, http.StatusInternalServerError, "DATASET_LOOKUP_FAILED", "could not look up dataset")
	return true
}

func datasetForResponse(dataset domain.Dataset) datasetResponse {
	return datasetResponse{
		ID: dataset.ID, Slug: dataset.Slug, Name: dataset.Name, Description: dataset.Description,
		SourceSpace: dataset.SourceSpace, SourceRelativePath: dataset.SourceRelativePath,
		OwnerTenantID: dataset.OwnerTenantID, Visibility: dataset.Visibility, SchemaVersion: dataset.SchemaVersion,
	}
}

func datasetVersionForResponse(version domain.DatasetVersion) datasetVersionResponse {
	return datasetVersionResponse{
		ID: version.ID, DatasetID: version.DatasetID, Version: version.Version, State: version.State,
		ManifestSHA256: version.ManifestSHA256, SchemaVersion: version.SchemaVersion,
		TrainSamples: version.TrainSamples, ValSamples: version.ValSamples, TestSamples: version.TestSamples,
		SourceObjectCount: version.SourceObjectCount, LogicalBytes: version.LogicalBytes, PackedBytes: version.PackedBytes,
	}
}

func datasetPublicationForResponse(run domain.DatasetPublicationRun) datasetPublicationResponse {
	return datasetPublicationResponse{
		ID: run.ID, DatasetID: run.DatasetID, DatasetVersionID: run.DatasetVersionID, State: run.State,
		TotalPartitions: run.TotalPartitions, CompletedPartitions: run.CompletedPartitions, FailedPartitions: run.FailedPartitions,
		SourceObjectCount: run.SourceObjectCount, ProcessedObjectCount: run.ProcessedObjectCount, FailedObjectCount: run.FailedObjectCount,
	}
}

func isInternalDatasetSourcePath(value, internalPrefix string) bool {
	value = strings.TrimSpace(value)
	internalPrefix = strings.TrimSuffix(strings.TrimSpace(internalPrefix), "/")
	if internalPrefix == "" {
		internalPrefix = internalDatasetObjectPrefix
	}
	return value == internalPrefix || strings.HasPrefix(value, internalPrefix+"/") || strings.Contains(value, "://")
}

func newDatasetID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random dataset ID: %w", err)
	}
	return "dataset-" + hex.EncodeToString(value), nil
}
