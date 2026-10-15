package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/personalstorage"
)

// PersonalStorageService accepts only an authenticated stable personal owner.
// Neither HTTP bodies nor team/administrator roles can select an owner.
type PersonalStorageService interface {
	View(context.Context, personalstorage.Owner) (personalstorage.View, error)
	Enroll(context.Context, personalstorage.Owner) (personalstorage.View, error)
	Verify(context.Context, personalstorage.Owner) (personalstorage.View, error)
	OpenStore(context.Context, personalstorage.Owner) (personalstorage.Store, error)
	MountReference(context.Context, personalstorage.Owner) (personalstorage.MountReference, error)
}

func personalStorageOwner(principal auth.Principal) (personalstorage.Owner, error) {
	if !auth.IsInteractiveAuthType(principal.AuthType) || principal.Subject == "" || strings.TrimSpace(principal.Subject) != principal.Subject || domain.ValidateUsername(principal.Username) != nil {
		return personalstorage.Owner{}, fmt.Errorf("an authenticated personal interactive identity is required")
	}
	return personalstorage.Owner{ID: principal.Subject, Username: principal.Username}, nil
}

func (h *Handler) RegisterPersonalStorageRoutes(group *gin.RouterGroup) {
	group.GET("/personal-storage/wellspiking", h.getPersonalStorage)
	group.POST("/personal-storage/wellspiking", h.enrollPersonalStorage)
	group.POST("/personal-storage/wellspiking/verify", h.verifyPersonalStorage)
}

type personalStorageResponse struct {
	Enabled          bool   `json:"enabled"`
	Bound            bool   `json:"bound"`
	ConnectionStatus string `json:"connectionStatus"`
	BrowseEnabled    bool   `json:"browseEnabled"`
	MountPath        string `json:"mountPath"`
	MountStatus      string `json:"mountStatus"`
	MountWorkspaceID string `json:"mountWorkspaceId"`
	PublicKey        string `json:"publicKey,omitempty"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	Username         string `json:"username,omitempty"`
	Generation       int64  `json:"generation,omitempty"`
	MaxUploadBytes   int64  `json:"maxUploadBytes"`
	RegistrationURL  string `json:"registrationURL"`
}

func (h *Handler) personalStoragePrincipal(c *gin.Context) (auth.Principal, personalstorage.Owner, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, personalstorage.Owner{}, false
	}
	owner, err := personalStorageOwner(principal)
	if err != nil {
		h.writeError(c, http.StatusForbidden, "PERSONAL_STORAGE_INTERACTIVE_REQUIRED", "personal storage requires your interactive login")
		return principal, owner, false
	}
	return principal, owner, true
}

// Empty deployment scope preserves globally attested mode. A nonempty scope
// is matched only against the stable authenticated subject, with no role bypass.
func (h *Handler) personalStorageAllowed(principal auth.Principal) bool {
	return h.personalStorage != nil && (len(h.personalStorageTestOwnerIDs) == 0 || h.personalStorageTestOwnerIDs[principal.Subject])
}

func (h *Handler) rejectPersonalStorageScope(c *gin.Context, principal auth.Principal) bool {
	if h.personalStorage != nil && !h.personalStorageAllowed(principal) {
		h.writeError(c, http.StatusForbidden, "PERSONAL_STORAGE_TEST_SCOPE_REQUIRED", "Wellspiking personal storage is not enabled for your account")
		return true
	}
	return false
}

func (h *Handler) personalStorageStatus(ctx context.Context, principal auth.Principal, view personalstorage.View) personalStorageResponse {
	response := personalStorageResponse{Enabled: h.personalStorageAllowed(principal), ConnectionStatus: "DISABLED", MountPath: domain.WellspikingPersonalMountPath, MountStatus: "not-mounted", MaxUploadBytes: h.personalStorageMaxUpload, RegistrationURL: "https://docs.wellspiking.ai/begin/storage/mount_linux.html"}
	if !response.Enabled {
		return response
	}
	response.Bound = view.BindingID != ""
	response.ConnectionStatus = view.State
	response.BrowseEnabled = view.State == "READY"
	response.PublicKey = view.PublicKey
	response.Fingerprint = view.Fingerprint
	response.Username = view.Username
	response.Generation = view.Generation
	if h.workspaces == nil {
		return response
	}
	workspace, err := h.workspaces.GetWorkspace(ctx, principal.TenantID, principal.Subject)
	if err != nil || workspace == nil || workspace.State == domain.WorkspaceStopped {
		return response
	}
	response.MountWorkspaceID = workspace.ID
	if workspace.State == domain.WorkspaceFailed {
		response.MountStatus = "failed"
		return response
	}
	if h.kubernetes == nil {
		response.MountStatus = "unavailable"
		return response
	}
	if view.State != "READY" {
		return response
	}
	reference, err := h.personalStorage.MountReference(ctx, personalstorage.Owner{ID: principal.Subject, Username: principal.Username})
	if err != nil {
		response.MountStatus = "unavailable"
		return response
	}
	status, err := h.kubernetes.PersonalStorageWorkspaceMountStatus(ctx, *workspace, reference, h.platformNamespace, h.personalStorageCapacity)
	if err != nil {
		response.MountStatus = "unavailable"
	} else {
		response.MountStatus = status
	}
	return response
}

func (h *Handler) getPersonalStorage(c *gin.Context) {
	principal, owner, ok := h.personalStoragePrincipal(c)
	if !ok {
		return
	}
	if !h.personalStorageAllowed(principal) {
		h.writeSuccess(c, http.StatusOK, h.personalStorageStatus(c.Request.Context(), principal, personalstorage.View{}))
		return
	}
	view, err := h.personalStorage.View(c.Request.Context(), owner)
	if err != nil {
		h.personalStorageError(c)
		return
	}
	h.writeSuccess(c, http.StatusOK, h.personalStorageStatus(c.Request.Context(), principal, view))
}

// Reject every request field: enrollment is entirely derived from the session.
func emptyPersonalStorageRequest(c *gin.Context) bool {
	if c.Request.Body == nil {
		return true
	}
	defer c.Request.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
	if err != nil || len(raw) > 1024 {
		return false
	}
	if strings.TrimSpace(string(raw)) == "" {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && fields != nil && len(fields) == 0
}

func (h *Handler) changePersonalStorage(c *gin.Context, verify bool) {
	principal, owner, ok := h.personalStoragePrincipal(c)
	if !ok {
		return
	}
	if !h.authorizePersonalStorageMutation(c, true) {
		return
	}
	if h.personalStorage == nil {
		h.writeError(c, http.StatusServiceUnavailable, "PERSONAL_STORAGE_DISABLED", "Wellspiking personal storage is not enabled")
		return
	}
	if !emptyPersonalStorageRequest(c) {
		h.writeError(c, http.StatusBadRequest, "PERSONAL_STORAGE_INVALID_REQUEST", "personal storage does not accept an owner, account or storage configuration")
		return
	}
	if h.repository == nil || h.repository.EnsureIdentity(c.Request.Context(), principal) != nil {
		h.writeError(c, http.StatusServiceUnavailable, "PERSONAL_STORAGE_IDENTITY_UNAVAILABLE", "could not confirm your platform identity")
		return
	}
	var view personalstorage.View
	var err error
	if verify {
		view, err = h.personalStorage.Verify(c.Request.Context(), owner)
	} else {
		view, err = h.personalStorage.Enroll(c.Request.Context(), owner)
	}
	if err != nil {
		h.personalStorageError(c)
		return
	}
	h.writeSuccess(c, http.StatusOK, h.personalStorageStatus(c.Request.Context(), principal, view))
}
func (h *Handler) enrollPersonalStorage(c *gin.Context) { h.changePersonalStorage(c, false) }
func (h *Handler) verifyPersonalStorage(c *gin.Context) { h.changePersonalStorage(c, true) }
func (h *Handler) personalStorageError(c *gin.Context) {
	h.writeError(c, http.StatusConflict, "PERSONAL_STORAGE_NOT_READY", "your personal Wellspiking storage is unavailable; register your public key and verify the connection")
}

func (h *Handler) isPersonalStorageRequest(c *gin.Context) bool {
	return c.Param("id") == string(domain.DataSpaceWellspikingPersonal)
}
func (h *Handler) openPersonalStorage(c *gin.Context) (personalstorage.Store, bool) {
	principal, owner, ok := h.personalStoragePrincipal(c)
	if !ok {
		return nil, false
	}
	if h.personalStorage == nil {
		h.writeError(c, http.StatusNotFound, "DATA_SPACE_NOT_FOUND", "data space was not found")
		return nil, false
	}
	if h.rejectPersonalStorageScope(c, principal) {
		return nil, false
	}
	store, err := h.personalStorage.OpenStore(c.Request.Context(), owner)
	if err != nil {
		h.personalStorageError(c)
		return nil, false
	}
	return store, true
}

func (h *Handler) listPersonalStorage(c *gin.Context, directoriesOnly bool) {
	store, ok := h.openPersonalStorage(c)
	if !ok {
		return
	}
	defer store.Close()
	relative, err := personalstorage.ValidatePath(c.Query("path"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "path must stay inside your personal directory")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 1 || limit > 1000 {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PAGE_LIMIT", "page limit must be between 1 and 1000")
		return
	}
	offset := 0
	if cursor := c.Query("cursor"); cursor != "" {
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_CURSOR", "page cursor is invalid")
			return
		}
	}
	entries, err := store.List(c.Request.Context(), relative)
	if err != nil {
		h.personalStorageError(c)
		return
	}
	page := objectstore.DataEntryPage{Entries: []objectstore.DataEntry{}}
	dirs := objectstore.DirectoryPage{Directories: []string{}}
	filtered := make([]personalstorage.Entry, 0, len(entries))
	for _, entry := range entries {
		if !directoriesOnly || entry.Mode.IsDir() {
			filtered = append(filtered, entry)
		}
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	if limit > len(filtered)-offset {
		limit = len(filtered) - offset
	}
	end := offset + limit
	for _, entry := range filtered[offset:end] {
		kind := objectstore.DataEntryFile
		if entry.Mode.IsDir() {
			kind = objectstore.DataEntryDirectory
			dirs.Directories = append(dirs.Directories, entry.Name)
		}
		page.Entries = append(page.Entries, objectstore.DataEntry{Name: entry.Name, Type: kind, SizeBytes: entry.Size, LastModified: entry.ModTime})
	}
	if end < len(filtered) {
		page.NextCursor = strconv.Itoa(end)
		dirs.NextCursor = page.NextCursor
	}
	if directoriesOnly {
		h.writeSuccess(c, http.StatusOK, dirs)
	} else {
		h.writeSuccess(c, http.StatusOK, page)
	}
}

func (h *Handler) mkdirPersonalStorage(c *gin.Context) {
	if !h.authorizePersonalStorageMutation(c, true) {
		return
	}
	store, ok := h.openPersonalStorage(c)
	if !ok {
		return
	}
	defer store.Close()
	var request createDataSpaceFolderRequest
	if c.ShouldBindJSON(&request) != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	relative, err := personalstorage.ValidatePath(request.Path)
	if err != nil || relative == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "folder path must stay inside your personal directory")
		return
	}
	if err = store.Mkdir(c.Request.Context(), relative); err != nil {
		h.personalStorageError(c)
		return
	}
	h.writeSuccess(c, http.StatusCreated, map[string]string{"path": relative})
}

func (h *Handler) ticketPersonalStorage(c *gin.Context) {
	if !h.authorizePersonalStorageMutation(c, true) {
		return
	}
	store, ok := h.openPersonalStorage(c)
	if !ok {
		return
	}
	defer store.Close()
	var request createDataSpaceUploadRequest
	if c.ShouldBindJSON(&request) != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	relative, err := personalstorage.ValidatePath(request.Path)
	if err != nil || relative == "" || request.SizeBytes == nil || *request.SizeBytes < 0 || strings.TrimSpace(request.ContentType) == "" || strings.ContainsAny(request.ContentType, "\r\n") {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_UPLOAD", "upload path, type or size is invalid")
		return
	}
	if *request.SizeBytes > h.personalStorageMaxUpload {
		h.writeError(c, http.StatusRequestEntityTooLarge, "DATA_SPACE_UPLOAD_TOO_LARGE", "this file exceeds the personal storage upload limit")
		return
	}
	h.writeSuccess(c, http.StatusCreated, dataSpaceUploadTicket{Mode: domain.DataSpaceUploadSingle, URL: "/api/v1/data-spaces/wellspiking-personal/content?path=" + url.QueryEscape(relative), ContentType: request.ContentType, SizeBytes: *request.SizeBytes})
}

func (h *Handler) uploadPersonalStorage(c *gin.Context) {
	if !h.authorizePersonalStorageMutation(c, false) {
		return
	}
	done, ok := h.boundPersonalStorageUpload(c)
	if !ok {
		return
	}
	defer done()
	store, ok := h.openPersonalStorage(c)
	if !ok {
		return
	}
	defer store.Close()
	relative, err := personalstorage.ValidatePath(c.Query("path"))
	if err != nil || relative == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "file path must stay inside your personal directory")
		return
	}
	size := c.Request.ContentLength
	if size < 0 || size > h.personalStorageMaxUpload {
		h.writeError(c, http.StatusRequestEntityTooLarge, "DATA_SPACE_UPLOAD_TOO_LARGE", "personal storage requires a bounded upload size")
		return
	}
	body := http.MaxBytesReader(c.Writer, c.Request.Body, size+1)
	defer body.Close()
	if err = personalstorage.Upload(c.Request.Context(), store, relative, body, size, h.personalStorageMaxUpload); err != nil {
		h.personalStorageError(c)
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]string{"path": relative})
}

// A non-simple header makes cookie-authenticated cross-origin mutations pass
// through the browser's CORS preflight. HTML forms cannot enroll or upload.
func (h *Handler) authorizePersonalStorageMutation(c *gin.Context, jsonBody bool) bool {
	principal, _, ok := h.personalStoragePrincipal(c)
	if !ok || h.rejectPersonalStorageScope(c, principal) {
		return false
	}
	if c.GetHeader("X-RayTrain-Personal-Storage") != "1" || c.GetHeader("Sec-Fetch-Site") == "cross-site" {
		h.writeError(c, http.StatusForbidden, "PERSONAL_STORAGE_CSRF_REQUIRED", "personal storage changes require the authenticated Portal request")
		return false
	}
	if jsonBody {
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "application/json" {
			h.writeError(c, http.StatusUnsupportedMediaType, "PERSONAL_STORAGE_JSON_REQUIRED", "personal storage changes require application/json")
			return false
		}
	}
	return true
}
