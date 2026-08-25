package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

// DataSpaceStore owns the private inventory of platform-created mount
// adapters. HTTP handlers only request scoped lookups and never expose the
// resulting claim names, prefixes, or CSI attributes.
type DataSpaceStore interface {
	EnsurePersonalDataBinding(context.Context, domain.DataMountBinding) (domain.DataMountBinding, error)
	ListDataBindings(context.Context, string, string) ([]domain.DataMountBinding, error)
}

type dataSpaceBindingStatusUpdater interface {
	UpdateDataBindingStatus(context.Context, string, domain.DataMountBindingStatus) error
}

type tenantSharedDataBindingEnsurer interface {
	EnsureTenantSharedDataBindings(context.Context, ...domain.DataMountBinding) ([]domain.DataMountBinding, error)
}

type tenantRootDataBindingEnsurer interface {
	EnsureTenantRootDataBinding(context.Context, domain.DataMountBinding) (domain.DataMountBinding, error)
}

type idcDataBindingEnsurer interface {
	EnsureIDCDataBindings(context.Context, ...domain.DataMountBinding) ([]domain.DataMountBinding, error)
}

type dataSpaceDirectoryInitializer interface {
	EnsureDataDirectory(context.Context, string) error
}

type dataSpaceResponse struct {
	ID            domain.DataSpaceID `json:"id"`
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	Provider      string             `json:"provider"`
	MountPath     string             `json:"mountPath"`
	ReadOnly      bool               `json:"readOnly"`
	CanWrite      bool               `json:"canWrite"`
	BrowseEnabled bool               `json:"browseEnabled"`
	StorageStatus string             `json:"storageStatus"`
	MountStatus   string             `json:"mountStatus"`
}

func (h *Handler) RegisterDataSpaceRoutes(group *gin.RouterGroup) {
	group.GET("/data-spaces", h.listDataSpaces)
	group.GET("/data-spaces/:id/directories", h.listDataSpaceDirectories)
	group.GET("/data-spaces/:id/entries", h.listDataSpaceEntries)
	group.POST("/data-spaces/:id/folders", h.createDataSpaceFolder)
	group.POST("/data-spaces/:id/uploads", h.createDataSpaceUpload)
}

const dataSpaceUploadTTL = 15 * time.Minute

func (h *Handler) listDataSpaces(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.dataSpaces == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACES_UNAVAILABLE", "data spaces are not configured")
		return
	}
	if err := h.ensureDataSpacesForPrincipal(c.Request.Context(), principal); err != nil {
		if errors.Is(err, errDataSpaceIdentityPersist) {
			h.writeError(c, http.StatusInternalServerError, "DATA_SPACE_IDENTITY_PERSIST_FAILED", "could not initialize the authenticated data-space identity")
		} else {
			h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_INITIALIZATION_FAILED", "could not initialize the selected data spaces; inspect platform storage readiness and try again")
		}
		return
	}
	spaces, err := h.personalDataSpacesForPrincipal(c.Request.Context(), principal)
	if err != nil {
		h.writeError(c, http.StatusForbidden, "DATA_SPACE_IDENTITY_INVALID", "the authenticated identity cannot access data spaces")
		return
	}
	bindings, err := h.dataSpaces.ListDataBindings(c.Request.Context(), principal.TenantID, principal.Subject)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DATA_SPACES_LIST_FAILED", "could not list data spaces")
		return
	}
	h.writeSuccess(c, http.StatusOK, dataSpacesForResponse(spaces, bindings, principal, h.dataObjectStore != nil, h.dataSpacesEnabled, h.idcDataSpacesEnabled))
}

var errDataSpaceIdentityPersist = errors.New("data-space identity persistence failed")

// ensureDataSpacesForPrincipal is shared by the data browser, direct
// workspace launch and task submission. A user must not need to visit one
// page first for a later training workflow to mount the correct data.
func (h *Handler) ensureDataSpacesForPrincipal(ctx context.Context, principal auth.Principal) error {
	if err := h.repository.EnsureIdentity(ctx, principal); err != nil {
		return fmt.Errorf("%w: %v", errDataSpaceIdentityPersist, err)
	}
	if err := h.ensureTenantNamespaceAndPullSecrets(ctx, principal.TenantID, "tenant-"+sanitizeDNS(principal.TenantID)); err != nil {
		return fmt.Errorf("prepare tenant runtime: %w", err)
	}
	// Object-space readiness is independent of CSI/FSX workload mounting. This
	// also backfills the fixed marker directories for accounts that predate
	// automatic provisioning, so every authenticated user sees the same
	// personal file/workspace/runs layout in the Portal.
	if h.dataObjectStore != nil && h.directoryInitializer != nil {
		root, err := h.personalDataRootForPrincipal(ctx, principal)
		if err != nil {
			return fmt.Errorf("derive personal object root: %w", err)
		}
		if err := h.directoryInitializer.EnsurePersonalDataDirectories(ctx, root); err != nil {
			return fmt.Errorf("initialize personal object space: %w", err)
		}
	}
	if !h.dataSpacesEnabled && !h.idcDataSpacesEnabled {
		return nil
	}
	if h.dataSpaces == nil {
		return fmt.Errorf("data-space repository is not configured")
	}
	// Object browsing and workload mounting are intentionally independent. A
	// cluster may safely expose a caller's logical TOS roots through the Portal
	// before the FSX CSI/IRSA contract is enabled, but it must never persist a
	// half-formed binding with an empty JSONB volume configuration.
	if h.dataSpacesEnabled {
		bindingID, err := h.newID()
		if err != nil {
			return fmt.Errorf("allocate personal data binding id: %w", err)
		}
		pending, err := h.newPersonalDataBinding(bindingID, principal)
		if err != nil {
			return err
		}
		binding, err := h.dataSpaces.EnsurePersonalDataBinding(ctx, pending)
		if err != nil {
			return fmt.Errorf("initialize personal data binding: %w", err)
		}
		h.ensurePersonalDataMount(ctx, binding)
		if err := h.ensureTenantSharedDataMounts(ctx, principal); err != nil {
			return err
		}
	}
	return h.ensureIDCDataMounts(ctx, principal)
}

func (h *Handler) ensureIDCDataMounts(ctx context.Context, principal auth.Principal) error {
	if !h.idcDataSpacesEnabled || h.kubernetes == nil {
		return nil
	}
	ensurer, ok := h.dataSpaces.(idcDataBindingEnsurer)
	if !ok {
		return fmt.Errorf("data-space repository cannot initialize IDC mounts")
	}
	tenantKey := dataMountTenantKey(principal.TenantID)
	requested := make([]domain.DataMountBinding, 0, 3)
	for _, item := range []struct {
		space domain.DataSpaceID
		name  string
	}{
		{space: domain.DataSpaceIDCOriginal, name: "original"},
		{space: domain.DataSpaceIDCWellspiking, name: "wellspiking"},
		{space: domain.DataSpaceIDCShared, name: "shared"},
	} {
		if _, ok := h.idcDataSpaceSources[item.space]; !ok {
			return fmt.Errorf("IDC source %s is not configured", item.name)
		}
		binding, err := domain.NewIDCDataMountBinding("idc-"+item.name+"-"+tenantKey, principal.TenantID, item.space, "idc-"+item.name+"-"+tenantKey)
		if err != nil {
			return err
		}
		requested = append(requested, binding)
	}
	bindings, err := ensurer.EnsureIDCDataBindings(ctx, requested...)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		binding = h.retryDataMountBinding(ctx, binding)
		source, ok := h.idcDataSpaceSources[binding.SpaceID]
		if !ok {
			h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
			continue
		}
		ready, ensureErr := h.kubernetes.EnsureIDCDataMountResources(ctx, binding, "tenant-"+sanitizeDNS(principal.TenantID), h.idcDataSpacesCapacity, source)
		if ensureErr != nil {
			h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
			continue
		}
		if ready {
			h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingReady)
		}
	}
	return nil
}

func (h *Handler) ensureTenantSharedDataMounts(ctx context.Context, principal auth.Principal) error {
	if !h.dataSpacesEnabled || h.kubernetes == nil {
		return nil
	}
	ensurer, ok := h.dataSpaces.(tenantSharedDataBindingEnsurer)
	if !ok {
		return fmt.Errorf("data-space repository cannot initialize shared mounts")
	}
	rootEnsurer, ok := h.dataSpaces.(tenantRootDataBindingEnsurer)
	if !ok {
		return fmt.Errorf("data-space repository cannot initialize the tenant storage root")
	}
	tenantKey := dataMountTenantKey(principal.TenantID)
	root, err := domain.NewTenantRootDataMountBinding("tos-root-"+tenantKey, principal.TenantID, "data-tenant-"+tenantKey, h.dataSpacesFSXAttrs)
	if err != nil {
		return err
	}
	root, err = rootEnsurer.EnsureTenantRootDataBinding(ctx, root)
	if err != nil {
		return err
	}
	directoryInitializer, ok := h.directoryInitializer.(dataSpaceDirectoryInitializer)
	if !ok {
		return fmt.Errorf("data-space directory initializer cannot prepare shared roots")
	}
	root = h.retryDataMountBinding(ctx, root)
	if err := directoryInitializer.EnsureDataDirectory(ctx, root.RootPrefix); err != nil {
		h.setPersonalDataMountStatus(ctx, root.ID, domain.DataMountBindingFailed)
		return err
	}
	ready, ensureErr := h.kubernetes.EnsureDataMountResources(ctx, root, "tenant-"+sanitizeDNS(principal.TenantID), h.dataSpacesCapacity)
	if ensureErr != nil {
		h.setPersonalDataMountStatus(ctx, root.ID, domain.DataMountBindingFailed)
		return ensureErr
	}
	if ready {
		h.setPersonalDataMountStatus(ctx, root.ID, domain.DataMountBindingReady)
	}
	teamID := "team-" + tenantKey
	publicID := "public-" + tenantKey
	team, err := domain.NewSharedDataMountBinding(teamID, principal.TenantID, domain.DataSpaceTeamShared, "data-team-"+tenantKey, h.dataSpacesFSXAttrs)
	if err != nil {
		return err
	}
	publicRoot, err := h.publicDataRootForTenant(principal.TenantID)
	if err != nil {
		return err
	}
	public, err := domain.NewPublicDataMountBinding(publicID, principal.TenantID, "data-public-"+tenantKey, h.dataSpacesFSXAttrs, publicRoot)
	if err != nil {
		return err
	}
	bindings, err := ensurer.EnsureTenantSharedDataBindings(ctx, team, public)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		binding = h.retryDataMountBinding(ctx, binding)
		if err := directoryInitializer.EnsureDataDirectory(ctx, binding.RootPrefix); err != nil {
			h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
			continue
		}
		ready, ensureErr := h.kubernetes.EnsureDataMountResources(ctx, binding, "tenant-"+sanitizeDNS(principal.TenantID), h.dataSpacesCapacity)
		if ensureErr != nil {
			h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
			continue
		}
		if ready {
			h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingReady)
		}
	}
	return nil
}

func dataMountTenantKey(tenantID string) string {
	digest := sha256.Sum256([]byte(tenantID))
	return hex.EncodeToString(digest[:])[:12]
}

func (h *Handler) newPersonalDataBinding(id string, principal auth.Principal) (domain.DataMountBinding, error) {
	if !h.dataSpacesEnabled {
		return domain.DataMountBinding{
			ID: id, TenantID: principal.TenantID, UserID: principal.Subject,
			Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, Status: domain.DataMountBindingPending,
		}, nil
	}
	claimName := "data-" + sanitizeDNS(id)
	return domain.NewPersonalDataMountBinding(id, principal.TenantID, principal.Subject, claimName, h.dataSpacesFSXAttrs, StorageKeyForPrincipal(principal))
}

func (h *Handler) ensurePersonalDataMount(ctx context.Context, binding domain.DataMountBinding) {
	if !h.dataSpacesEnabled || h.kubernetes == nil {
		return
	}
	binding = h.retryDataMountBinding(ctx, binding)
	if h.directoryInitializer == nil {
		h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
		return
	}
	if err := h.directoryInitializer.EnsurePersonalDataDirectories(ctx, binding.RootPrefix); err != nil {
		h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
		return
	}
	ready, err := h.kubernetes.EnsureDataMountResources(ctx, binding, "tenant-"+sanitizeDNS(binding.TenantID), h.dataSpacesCapacity)
	if err != nil {
		h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingFailed)
		return
	}
	if ready {
		h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingReady)
	}
}

// A failed mount is a point-in-time infrastructure result, not an immutable
// user state. When an operator restores NFS/CSI connectivity the next Portal
// refresh retries only the same owned PV/PVC and never adopts a foreign one.
func (h *Handler) retryDataMountBinding(ctx context.Context, binding domain.DataMountBinding) domain.DataMountBinding {
	if binding.Status == domain.DataMountBindingFailed {
		h.setPersonalDataMountStatus(ctx, binding.ID, domain.DataMountBindingPending)
		binding.Status = domain.DataMountBindingPending
	}
	return binding
}

func (h *Handler) setPersonalDataMountStatus(ctx context.Context, bindingID string, status domain.DataMountBindingStatus) {
	updater, ok := h.dataSpaces.(dataSpaceBindingStatusUpdater)
	if ok {
		_ = updater.UpdateDataBindingStatus(ctx, bindingID, status)
	}
}

func (h *Handler) listDataSpaceDirectories(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	space, ok := h.dataSpaceForPrincipal(c, principal, domain.DataSpaceID(c.Param("id")))
	if !ok {
		return
	}
	if space.Provider != domain.StorageProviderTOS || !space.BrowseEnabled {
		h.writeError(c, http.StatusConflict, "DATA_SPACE_BROWSER_NOT_AVAILABLE", "this data space is available in workloads but cannot be browsed in the portal")
		return
	}
	if h.directoryLister == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_BROWSER_UNAVAILABLE", "data-space browser is not configured")
		return
	}
	relativePath, err := domain.NormalizeStorageRelativePath(c.Query("path"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "directory path must stay inside the selected data space")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 0 {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PAGE_LIMIT", "directory page limit is invalid")
		return
	}
	page, err := h.directoryLister.ListDirectories(c.Request.Context(), space.RootPrefix, relativePath, c.Query("cursor"), limit)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_BROWSER_UNAVAILABLE", "could not browse this data space")
		return
	}
	h.writeSuccess(c, http.StatusOK, page)
}

func (h *Handler) listDataSpaceEntries(c *gin.Context) {
	principal, space, ok := h.authorizeDataSpace(c)
	if !ok {
		return
	}
	if space.Provider != domain.StorageProviderTOS || !space.BrowseEnabled {
		h.writeError(c, http.StatusConflict, "DATA_SPACE_BROWSER_NOT_AVAILABLE", "this data space is available in workloads but cannot be browsed in the portal")
		return
	}
	if h.dataObjectStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "data-space storage is not configured")
		return
	}
	relativePath, err := domain.NormalizeStorageRelativePath(c.Query("path"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "directory path must stay inside the selected data space")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 0 {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PAGE_LIMIT", "data-space page limit is invalid")
		return
	}
	page, err := h.dataObjectStore.ListDataEntries(c.Request.Context(), space.RootPrefix, relativePath, c.Query("cursor"), limit)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "could not browse this data space")
		return
	}
	_ = principal
	h.writeSuccess(c, http.StatusOK, page)
}

type createDataSpaceFolderRequest struct {
	Path string `json:"path"`
}

func (h *Handler) createDataSpaceFolder(c *gin.Context) {
	_, space, ok := h.authorizeWritableDataSpace(c)
	if !ok {
		return
	}
	if h.dataObjectStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "data-space storage is not configured")
		return
	}
	var request createDataSpaceFolderRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	relativePath, err := domain.NormalizeStorageRelativePath(request.Path)
	if err != nil || relativePath == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "folder path must stay inside the selected writable data space")
		return
	}
	if err := h.dataObjectStore.CreateDataDirectory(c.Request.Context(), space.RootPrefix, relativePath); err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "could not create folder")
		return
	}
	h.writeSuccess(c, http.StatusCreated, map[string]string{"path": relativePath})
}

type createDataSpaceUploadRequest struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	SizeBytes   *int64 `json:"sizeBytes"`
}

func (h *Handler) createDataSpaceUpload(c *gin.Context) {
	_, space, ok := h.authorizeWritableDataSpace(c)
	if !ok {
		return
	}
	if h.dataObjectStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "data-space storage is not configured")
		return
	}
	var request createDataSpaceUploadRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	relativePath, err := domain.NormalizeStorageRelativePath(request.Path)
	if err != nil || relativePath == "" || strings.HasPrefix(path.Base(relativePath), ".ray-train-") || strings.ContainsAny(request.ContentType, "\r\n") || strings.TrimSpace(request.ContentType) == "" || request.SizeBytes == nil || *request.SizeBytes < 0 {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_UPLOAD", "upload path, content type, or size is invalid")
		return
	}
	presigned, err := h.dataObjectStore.PresignDataPut(c.Request.Context(), space.RootPrefix, relativePath, strings.TrimSpace(request.ContentType), *request.SizeBytes, dataSpaceUploadTTL)
	if errors.Is(err, objectstore.ErrUnavailable) {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "data-space storage is temporarily unavailable")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_UPLOAD", "upload path or size is invalid")
		return
	}
	h.writeSuccess(c, http.StatusCreated, presigned)
}

func (h *Handler) authorizeDataSpace(c *gin.Context) (auth.Principal, domain.DataSpace, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, domain.DataSpace{}, false
	}
	space, ok := h.dataSpaceForPrincipal(c, principal, domain.DataSpaceID(c.Param("id")))
	if !ok {
		return auth.Principal{}, domain.DataSpace{}, false
	}
	return principal, space, true
}

func (h *Handler) authorizeWritableDataSpace(c *gin.Context) (auth.Principal, domain.DataSpace, bool) {
	principal, space, ok := h.authorizeDataSpace(c)
	if !ok {
		return auth.Principal{}, domain.DataSpace{}, false
	}
	if !canWriteDataSpace(space, principal) {
		h.writeError(c, http.StatusForbidden, "DATA_SPACE_READ_ONLY", "this data space is read-only")
		return auth.Principal{}, domain.DataSpace{}, false
	}
	return principal, space, true
}

func (h *Handler) dataSpaceForPrincipal(c *gin.Context, principal auth.Principal, id domain.DataSpaceID) (domain.DataSpace, bool) {
	spaces, err := h.personalDataSpacesForPrincipal(c.Request.Context(), principal)
	if err != nil {
		h.writeError(c, http.StatusForbidden, "DATA_SPACE_IDENTITY_INVALID", "the authenticated identity cannot access data spaces")
		return domain.DataSpace{}, false
	}
	space, found := domain.FindDataSpace(spaces, id)
	if !found {
		h.writeError(c, http.StatusNotFound, "DATA_SPACE_NOT_FOUND", "data space was not found")
		return domain.DataSpace{}, false
	}
	return space, true
}

// StorageKeyForPrincipal is only used while provisioning a previously unseen
// personal binding. Once a binding exists, its persisted storage key/root is
// authoritative, so a later display-name change cannot redirect a user to a
// different TOS prefix. Invalid IdP display names deliberately fall back to
// the opaque subject rather than creating an unsafe object path.
func StorageKeyForPrincipal(principal auth.Principal) string {
	username := domain.NormalizeUsername(principal.Username)
	if domain.ValidateUsername(username) == nil {
		return username
	}
	return principal.Subject
}

func (h *Handler) personalDataRootForPrincipal(ctx context.Context, principal auth.Principal) (string, error) {
	if h.dataSpaces != nil {
		bindings, err := h.dataSpaces.ListDataBindings(ctx, principal.TenantID, principal.Subject)
		if err != nil {
			return "", fmt.Errorf("list personal data bindings: %w", err)
		}
		for _, binding := range bindings {
			if binding.Scope != domain.DataMountScopePersonal || binding.SpaceID != domain.DataSpaceWorkspace || binding.TenantID != principal.TenantID || binding.UserID != principal.Subject || binding.RootPrefix == "" {
				continue
			}
			if _, err := domain.PersonalDataSpacesForRoot(principal.TenantID, binding.RootPrefix); err != nil {
				return "", fmt.Errorf("validate personal data binding root: %w", err)
			}
			return binding.RootPrefix, nil
		}
	}
	return domain.PersonalDataRootFor(principal.TenantID, StorageKeyForPrincipal(principal))
}

func (h *Handler) personalDataSpacesForPrincipal(ctx context.Context, principal auth.Principal) ([]domain.DataSpace, error) {
	root, err := h.personalDataRootForPrincipal(ctx, principal)
	if err != nil {
		return nil, err
	}
	publicRoot, err := h.publicDataRootForTenant(principal.TenantID)
	if err != nil {
		return nil, err
	}
	return domain.PersonalDataSpacesForRoots(principal.TenantID, root, publicRoot)
}

func (h *Handler) publicDataRootForTenant(tenantID string) (string, error) {
	root := h.dataSpacesPublicRoot
	if root == "" {
		root = domain.DefaultPublicDataRoot
	}
	return domain.PublicDataRootForTenant(tenantID, root)
}

func dataSpacesForResponse(spaces []domain.DataSpace, bindings []domain.DataMountBinding, principal auth.Principal, tosStorageAvailable, tosMountingEnabled, idcMountingEnabled bool) []dataSpaceResponse {
	statuses := dataSpaceBindingStatuses(bindings)
	response := make([]dataSpaceResponse, 0, len(spaces))
	for _, space := range spaces {
		mountStatus := "not-configured"
		if dataSpaceMountingEnabled(space.ID, tosMountingEnabled, idcMountingEnabled) {
			mountStatus = dataSpaceMountStatus(space.ID, statuses)
		}
		response = append(response, dataSpaceResponse{
			ID: space.ID, Name: space.Name, Description: space.Description, Provider: space.Provider,
			MountPath: space.MountPath, ReadOnly: space.ReadOnly, CanWrite: canWriteDataSpace(space, principal), BrowseEnabled: space.BrowseEnabled,
			StorageStatus: dataSpaceStorageStatus(space, tosStorageAvailable),
			MountStatus:   mountStatus,
		})
	}
	return response
}

func canWriteDataSpace(space domain.DataSpace, principal auth.Principal) bool {
	if space.Provider != domain.StorageProviderTOS || (!principal.HasRole(domain.RoleEngineer) && !principal.HasRole(domain.RoleTenantAdmin) && !principal.HasRole(domain.RoleSuperAdmin)) {
		return false
	}
	return domain.CanManageDataSpace(
		space.ID,
		principal.HasRole(domain.RoleTenantAdmin),
		principal.HasRole(domain.RoleSuperAdmin),
	)
}

func dataSpaceStorageStatus(space domain.DataSpace, tosStorageAvailable bool) string {
	if space.Provider != domain.StorageProviderTOS {
		return "not-applicable"
	}
	if tosStorageAvailable {
		return "ready"
	}
	return "not-configured"
}

func dataSpaceMountingEnabled(space domain.DataSpaceID, tosMountingEnabled, idcMountingEnabled bool) bool {
	switch space {
	case domain.DataSpaceIDCOriginal, domain.DataSpaceIDCWellspiking, domain.DataSpaceIDCShared:
		return idcMountingEnabled
	default:
		return tosMountingEnabled
	}
}

func dataSpaceBindingStatuses(bindings []domain.DataMountBinding) map[domain.DataSpaceID]domain.DataMountBindingStatus {
	statuses := make(map[domain.DataSpaceID]domain.DataMountBindingStatus, len(bindings))
	for _, binding := range bindings {
		// ListDataBindings is scoped by the repository; retaining the strongest
		// state avoids a stale pending duplicate making a ready shared root look
		// unavailable to the user.
		current, exists := statuses[binding.SpaceID]
		if !exists || mountStatusRank(binding.Status) > mountStatusRank(current) {
			statuses[binding.SpaceID] = binding.Status
		}
	}
	return statuses
}

func dataSpaceMountStatus(id domain.DataSpaceID, statuses map[domain.DataSpaceID]domain.DataMountBindingStatus) string {
	bindingSpace := id
	if id == domain.DataSpaceMyStorage || id == domain.DataSpaceMyFiles || id == domain.DataSpaceMyRuns {
		bindingSpace = domain.DataSpaceWorkspace
	}
	status, ok := statuses[bindingSpace]
	if !ok {
		return "not-configured"
	}
	return strings.ToLower(string(status))
}

func mountStatusRank(status domain.DataMountBindingStatus) int {
	switch status {
	case domain.DataMountBindingReady:
		return 3
	case domain.DataMountBindingPending:
		return 2
	case domain.DataMountBindingFailed:
		return 1
	default:
		return 0
	}
}
