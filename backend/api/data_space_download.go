package api

import (
	"io"
	"net/http"
	"path"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

// downloadDataSpaceFile lets a user retrieve a checkpoint they trained from the
// space it was written to. Training runs choose their own output directory, so
// the weights are often reachable only by browsing personal storage rather than
// through a task's artifact page.
//
// Two limits keep this from becoming a general export. Only the caller's own
// writable spaces qualify, so read-only team, public and IDC roots stay closed
// exactly as before. And only checkpoint files are served, so ordinary training
// data in a personal space is not downloadable either.
//
// The body is streamed rather than buffered: a checkpoint is routinely several
// gigabytes and must not sit in backend memory.
func (h *Handler) downloadDataSpaceFile(c *gin.Context) {
	_, space, ok := h.authorizeDataSpace(c)
	if !ok {
		return
	}
	if !domain.IsWritableDataSpace(space.ID) || space.ReadOnly {
		h.writeError(c, http.StatusForbidden, "DATA_SPACE_DOWNLOAD_FORBIDDEN", "only files in your own space may be downloaded")
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
	if err != nil || relativePath == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_PATH", "file path must stay inside the selected data space")
		return
	}
	contentType, allowed := downloadPolicy(relativePath)
	if !allowed {
		h.writeError(c, http.StatusUnsupportedMediaType, "DATA_SPACE_DOWNLOAD_UNSUPPORTED", "only trained checkpoints may be downloaded")
		return
	}
	file, err := h.dataObjectStore.ReadData(c.Request.Context(), space.RootPrefix, relativePath)
	if err != nil {
		h.writeError(c, http.StatusNotFound, "DATA_SPACE_FILE_NOT_FOUND", "this file was not found in the selected data space")
		return
	}
	defer file.Content.Close()

	writeCheckpointDownloadHeaders(c, contentType, path.Base(relativePath), file.SizeBytes)
	c.Status(http.StatusOK)
	// A copy failure here means the client hung up or the object store stopped
	// mid-stream. The status line is already sent, so there is no error envelope
	// left to write and the truncated body is what signals the failure.
	_, _ = io.Copy(c.Writer, file.Content)
}
