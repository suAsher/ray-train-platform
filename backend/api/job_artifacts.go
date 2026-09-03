package api

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

const maxArtifactPreviewBytes int64 = 5 * 1024 * 1024

func (h *Handler) listJobArtifacts(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, ok := h.authorizedArtifactJob(c, principal)
	if !ok {
		return
	}
	if h.artifactLister == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ARTIFACTS_UNAVAILABLE", "training artifacts are not configured")
		return
	}
	taskRoot, ok := h.jobArtifactRoot(c, principal, job)
	if !ok {
		return
	}
	path, err := domain.NormalizeStorageRelativePath(c.Query("path"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_ARTIFACT_PATH", "artifact path must be relative to this task output")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	page, err := h.artifactLister.ListArtifactEntries(c.Request.Context(), taskRoot, path, c.Query("cursor"), limit)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "ARTIFACT_BROWSER_UNAVAILABLE", "could not browse training artifacts")
		return
	}
	h.writeSuccess(c, http.StatusOK, page)
}

func (h *Handler) previewJobArtifact(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, ok := h.authorizedArtifactJob(c, principal)
	if !ok {
		return
	}
	if h.artifactReader == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ARTIFACTS_UNAVAILABLE", "training artifacts are not configured")
		return
	}
	taskRoot, ok := h.jobArtifactRoot(c, principal, job)
	if !ok {
		return
	}
	relativePath, ok := h.artifactFilePath(c)
	if !ok {
		return
	}
	kind, contentType, allowed := previewPolicy(relativePath)
	if !allowed {
		h.writeError(c, http.StatusUnsupportedMediaType, "ARTIFACT_PREVIEW_UNSUPPORTED", "this artifact type cannot be previewed")
		return
	}
	artifact, ok := h.readArtifact(c, taskRoot, relativePath)
	if !ok {
		return
	}
	defer artifact.Content.Close()
	if artifact.SizeBytes > maxArtifactPreviewBytes {
		h.writeError(c, http.StatusRequestEntityTooLarge, "ARTIFACT_PREVIEW_TOO_LARGE", "this artifact is too large to preview")
		return
	}
	contents, err := io.ReadAll(io.LimitReader(artifact.Content, maxArtifactPreviewBytes+1))
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "ARTIFACT_READ_FAILED", "could not read training artifact")
		return
	}
	if int64(len(contents)) > maxArtifactPreviewBytes {
		h.writeError(c, http.StatusRequestEntityTooLarge, "ARTIFACT_PREVIEW_TOO_LARGE", "this artifact is too large to preview")
		return
	}
	if kind == "text" {
		if !utf8.Valid(contents) {
			h.writeError(c, http.StatusUnsupportedMediaType, "ARTIFACT_PREVIEW_UNSUPPORTED", "this artifact is not UTF-8 text")
			return
		}
		h.writeSuccess(c, http.StatusOK, map[string]string{"kind": kind, "contentType": contentType, "content": string(contents)})
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]string{"kind": kind, "contentType": contentType, "content": base64.StdEncoding.EncodeToString(contents)})
}

// downloadPolicy decides which artifacts may leave the platform. Preview keeps
// its own allowlist for what a browser can render; download is about model
// weights, so it admits the checkpoint formats and nothing else. Anything a
// user only wants to look at should stay on the preview path.
//
// Access is the same as browsing: a caller reaches only their own task output.
func downloadPolicy(relativePath string) (contentType string, allowed bool) {
	switch strings.ToLower(path.Ext(relativePath)) {
	case ".pth", ".pt", ".ckpt", ".onnx", ".safetensors":
		return "application/octet-stream", true
	default:
		return "", false
	}
}

// downloadJobArtifact streams a trained checkpoint back to its owner.
//
// The body is copied straight from the object store rather than buffered, so a
// multi-gigabyte checkpoint does not sit in backend memory. Ownership and the
// task output root are resolved exactly as they are for browsing, so a caller
// still cannot reach another tenant's artifacts or escape the task prefix, and
// the object key never crosses the HTTP boundary.
func (h *Handler) downloadJobArtifact(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, ok := h.authorizedArtifactJob(c, principal)
	if !ok {
		return
	}
	if h.artifactReader == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ARTIFACTS_UNAVAILABLE", "training artifacts are not configured")
		return
	}
	taskRoot, ok := h.jobArtifactRoot(c, principal, job)
	if !ok {
		return
	}
	relativePath, ok := h.artifactFilePath(c)
	if !ok {
		return
	}
	contentType, allowed := downloadPolicy(relativePath)
	if !allowed {
		h.writeError(c, http.StatusUnsupportedMediaType, "ARTIFACT_DOWNLOAD_UNSUPPORTED", "only trained checkpoints may be downloaded")
		return
	}
	artifact, ok := h.readArtifact(c, taskRoot, relativePath)
	if !ok {
		return
	}
	defer artifact.Content.Close()

	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", "attachment; filename=\""+path.Base(relativePath)+"\"")
	c.Header("X-Content-Type-Options", "nosniff")
	if artifact.SizeBytes > 0 {
		c.Header("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, artifact.Content); err != nil {
		// The status line is already sent, so the truncated body is the only
		// signal available to the client.
		return
	}
}

func (h *Handler) authorizedArtifactJob(c *gin.Context, principal auth.Principal) (*domain.TrainingJob, bool) {
	job, err := h.repository.Get(c.Request.Context(), principal.TenantID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return nil, false
	}
	return job, true
}

func (h *Handler) artifactFilePath(c *gin.Context) (string, bool) {
	relativePath, err := domain.NormalizeStorageRelativePath(c.Query("path"))
	if err != nil || relativePath == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_ARTIFACT_PATH", "artifact path must be a file below this task output")
		return "", false
	}
	return relativePath, true
}

func (h *Handler) readArtifact(c *gin.Context, taskRoot, relativePath string) (objectstore.ArtifactRead, bool) {
	artifact, err := h.artifactReader.ReadArtifact(c.Request.Context(), taskRoot, relativePath)
	if errors.Is(err, objectstore.ErrNotFound) {
		h.writeError(c, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "training artifact was not found")
		return objectstore.ArtifactRead{}, false
	}
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "ARTIFACT_READ_FAILED", "could not read training artifact")
		return objectstore.ArtifactRead{}, false
	}
	return artifact, true
}

func previewPolicy(relativePath string) (kind, contentType string, allowed bool) {
	extension := strings.ToLower(path.Ext(relativePath))
	switch extension {
	case ".txt", ".log", ".json", ".jsonl", ".yaml", ".yml", ".csv", ".md", ".py", ".sh", ".ini", ".cfg":
		return "text", "text/plain; charset=utf-8", true
	case ".png":
		return "image", "image/png", true
	case ".jpg", ".jpeg":
		return "image", "image/jpeg", true
	case ".gif":
		return "image", "image/gif", true
	case ".webp":
		return "image", "image/webp", true
	default:
		return "", "", false
	}
}

// jobArtifactRoot resolves the only object-store prefix a caller may browse.
// It is derived from the persisted, server-resolved output mount and its
// catalogue asset; no HTTP input can select a bucket, PVC, or sibling task.
func (h *Handler) jobArtifactRoot(c *gin.Context, principal auth.Principal, job *domain.TrainingJob) (string, bool) {
	if root, ok := h.logicalJobArtifactRoot(c, principal, job); ok {
		return root, true
	}
	if job != nil && job.Spec.ResolvedDataMounts.Output != nil {
		if job.UserID != principal.Subject {
			h.writeError(c, http.StatusForbidden, "ARTIFACTS_FORBIDDEN", "personal training artifacts belong only to the submitting user")
		} else {
			h.writeError(c, http.StatusConflict, "ARTIFACTS_NOT_CONFIGURED", "this task has no browsable output storage")
		}
		return "", false
	}
	return h.legacyJobArtifactRoot(c, principal, job)
}

func (h *Handler) logicalJobArtifactRoot(c *gin.Context, principal auth.Principal, job *domain.TrainingJob) (string, bool) {
	if job == nil || job.UserID != principal.Subject {
		return "", false
	}
	output := job.Spec.ResolvedDataMounts.Output
	if output == nil || output.Space != domain.DataSpaceMyRuns || output.BindingSpace != domain.DataSpaceWorkspace || output.ClaimName == "" || output.ReadOnly || output.MountPath != domain.DataMountOutputPath {
		return "", false
	}
	spaces, err := h.personalDataSpacesForPrincipal(c.Request.Context(), principal)
	if err != nil {
		return "", false
	}
	runs, found := domain.FindDataSpace(spaces, domain.DataSpaceMyRuns)
	if !found {
		return "", false
	}
	relativePath, err := trainingOutputRelativePath(output.SubPath, runs.RootPrefix)
	if err != nil || (relativePath != job.ID && !strings.HasSuffix(relativePath, "/"+job.ID)) {
		return "", false
	}
	root := strings.TrimSuffix(runs.RootPrefix, "/") + "/" + relativePath
	if _, err := domain.NormalizeStorageRelativePath(root); err != nil {
		return "", false
	}
	return root, true
}

func (h *Handler) legacyJobArtifactRoot(c *gin.Context, principal auth.Principal, job *domain.TrainingJob) (string, bool) {
	if h.storageAssets == nil {
		h.writeError(c, http.StatusServiceUnavailable, "ARTIFACTS_UNAVAILABLE", "training artifacts are not configured")
		return "", false
	}
	output := job.Spec.ResolvedStorage.Output
	expectedRelativePath := "runs/" + job.ID
	if output == nil || output.AssetID == "" || output.ClaimName == "" || output.ReadOnly || output.MountPath != domain.StorageMountOutput || output.RelativePath != expectedRelativePath {
		h.writeError(c, http.StatusConflict, "ARTIFACTS_NOT_CONFIGURED", "this task has no browsable output storage")
		return "", false
	}
	asset, err := h.storageAssets.GetStorageAsset(c.Request.Context(), principal.TenantID, principal.Subject, output.AssetID)
	if errors.Is(err, repositories.ErrStorageAssetNotFound) {
		h.writeError(c, http.StatusNotFound, "ARTIFACTS_NOT_FOUND", "training artifact storage was not found")
		return "", false
	}
	if err != nil || asset.Kind != domain.StorageAssetOutput || asset.Provider != domain.StorageProviderTOS || asset.ReadOnly || !asset.BrowseEnabled || asset.ClaimName != output.ClaimName {
		h.writeError(c, http.StatusConflict, "ARTIFACTS_NOT_CONFIGURED", "this task output cannot be browsed")
		return "", false
	}
	asset, err = asset.Canonical()
	if err != nil {
		h.writeError(c, http.StatusConflict, "ARTIFACTS_NOT_CONFIGURED", "this task output cannot be browsed")
		return "", false
	}
	root := strings.TrimSuffix(asset.RootPrefix, "/") + "/" + expectedRelativePath
	if _, err := domain.NormalizeStorageRelativePath(root); err != nil {
		h.writeError(c, http.StatusConflict, "ARTIFACTS_NOT_CONFIGURED", "this task output cannot be browsed")
		return "", false
	}
	return root, true
}
