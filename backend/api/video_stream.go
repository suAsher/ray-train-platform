package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

func (h *Handler) videoDataSpaceFile(c *gin.Context) {
	_, space, ok := h.authorizeDataSpace(c)
	if !ok {
		return
	}
	if space.Provider != domain.StorageProviderTOS || !space.BrowseEnabled {
		h.writeError(c, http.StatusConflict, "DATA_SPACE_BROWSER_NOT_AVAILABLE", "this data space cannot be browsed in the portal")
		return
	}
	reader, _ := h.dataObjectStore.(objectstore.MediaReader)
	h.serveVideo(c, reader, space.RootPrefix)
}

func (h *Handler) videoJobArtifact(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, ok := h.authorizedArtifactJob(c, p)
	if !ok {
		return
	}
	root, ok := h.jobArtifactRoot(c, p, job)
	if !ok {
		return
	}
	reader, _ := h.artifactReader.(objectstore.MediaReader)
	h.serveVideo(c, reader, root)
}

func (h *Handler) serveVideo(c *gin.Context, reader objectstore.MediaReader, root string) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	relative, err := domain.NormalizeStorageRelativePath(c.Query("path"))
	if err != nil || relative == "" {
		h.writeError(c, http.StatusBadRequest, "INVALID_VIDEO_PATH", "video path must stay inside the selected storage")
		return
	}
	contentType := videoContentType(relative)
	if contentType == "" {
		h.writeError(c, http.StatusUnsupportedMediaType, "VIDEO_UNSUPPORTED", "only MP4 and WebM videos may be played")
		return
	}
	if reader == nil {
		h.writeError(c, http.StatusServiceUnavailable, "VIDEO_UNAVAILABLE", "video playback is not configured")
		return
	}
	info, err := reader.HeadMedia(c.Request.Context(), root, relative)
	if err != nil {
		h.videoReadError(c, err)
		return
	}
	if info.SizeBytes < 0 || !validVideoETag(info.ETag) {
		h.videoReadError(c, objectstore.ErrUnavailable)
		return
	}
	start, end, partial, ok := videoRange(c.Request, info)
	if !ok {
		c.Header("Content-Range", fmt.Sprintf("bytes */%d", info.SizeBytes))
		c.Header("Accept-Ranges", "bytes")
		h.writeError(c, http.StatusRequestedRangeNotSatisfiable, "VIDEO_RANGE_INVALID", "requested video range is not satisfiable")
		return
	}
	length := info.SizeBytes
	if partial {
		length = end - start + 1
	}
	if c.Request.Method == http.MethodHead || length == 0 {
		writeVideoHeaders(c, info, contentType, start, end, length, partial)
		c.Status(http.StatusOK)
		return
	}
	stream, err := reader.ReadMediaRange(c.Request.Context(), root, relative, info, start, end)
	if err != nil {
		h.videoReadError(c, err)
		return
	}
	if stream.Content == nil || stream.SizeBytes != length || stream.ETag != info.ETag {
		if stream.Content != nil {
			_ = stream.Content.Close()
		}
		h.videoReadError(c, objectstore.ErrUnavailable)
		return
	}
	var once sync.Once
	closeBody := func() { once.Do(func() { _ = stream.Content.Close() }) }
	stop := context.AfterFunc(c.Request.Context(), closeBody)
	defer func() { stop(); closeBody() }()
	writeVideoHeaders(c, info, contentType, start, end, length, partial)
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
	}
	c.Status(status)
	// CopyN bounds total bytes; wrapping the reader bounds each read and avoids
	// provider WriterTo implementations buffering an entire object.
	_, _ = io.CopyN(c.Writer, videoChunkReader{stream.Content}, length)
}

type videoChunkReader struct{ io.Reader }

func (r videoChunkReader) Read(p []byte) (int, error) {
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	return r.Reader.Read(p)
}

func writeVideoHeaders(c *gin.Context, info objectstore.MediaInfo, contentType string, start, end, length int64, partial bool) {
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", "inline")
	c.Header("Content-Length", strconv.FormatInt(length, 10))
	c.Header("ETag", info.ETag)
	if !info.LastModified.IsZero() {
		c.Header("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	}
	if partial {
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, info.SizeBytes))
	}
}

func (h *Handler) videoReadError(c *gin.Context, err error) {
	if errors.Is(err, objectstore.ErrNotFound) {
		h.writeError(c, http.StatusNotFound, "VIDEO_NOT_FOUND", "video was not found")
		return
	}
	if errors.Is(err, objectstore.ErrMediaChanged) {
		h.writeError(c, http.StatusPreconditionFailed, "VIDEO_CHANGED", "video changed; reopen the player to reload it")
		return
	}
	h.writeError(c, http.StatusBadGateway, "VIDEO_READ_FAILED", "could not read video")
}

func videoContentType(relative string) string {
	switch strings.ToLower(path.Ext(relative)) {
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	}
	return ""
}

func validVideoETag(etag string) bool {
	if len(etag) < 3 || len(etag) > 1024 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		return false
	}
	for _, b := range []byte(etag[1 : len(etag)-1]) {
		if b < 33 || b > 126 || b == '"' {
			return false
		}
	}
	return true
}

func videoRange(req *http.Request, info objectstore.MediaInfo) (int64, int64, bool, bool) {
	// Range only applies to GET. HEAD describes the complete representation.
	r := req.Header.Get("Range")
	if req.Method != http.MethodGet || r == "" {
		return 0, info.SizeBytes - 1, false, true
	}
	if ir := req.Header.Get("If-Range"); ir != "" && ir != info.ETag {
		date, err := http.ParseTime(ir)
		if err != nil || info.LastModified.IsZero() || !info.LastModified.Truncate(time.Second).Equal(date) {
			return 0, info.SizeBytes - 1, false, true
		}
	}
	if len(req.Header.Values("Range")) != 1 || !strings.HasPrefix(r, "bytes=") || strings.Contains(r, ",") || info.SizeBytes == 0 {
		return 0, 0, false, false
	}
	parts := strings.Split(strings.TrimPrefix(r, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false, false
	}
	parse := func(s string) (int64, error) {
		if s == "" {
			return 0, fmt.Errorf("empty range")
		}
		for _, b := range []byte(s) {
			if b < '0' || b > '9' {
				return 0, fmt.Errorf("invalid range")
			}
		}
		return strconv.ParseInt(s, 10, 64)
	}
	if parts[0] == "" {
		n, err := parse(parts[1])
		if err != nil || n == 0 {
			return 0, 0, false, false
		}
		if n > info.SizeBytes {
			n = info.SizeBytes
		}
		return info.SizeBytes - n, info.SizeBytes - 1, true, true
	}
	start, err := parse(parts[0])
	if err != nil || start >= info.SizeBytes {
		return 0, 0, false, false
	}
	end := info.SizeBytes - 1
	if parts[1] != "" {
		end, err = parse(parts[1])
		if err != nil || end < start {
			return 0, 0, false, false
		}
		if end >= info.SizeBytes {
			end = info.SizeBytes - 1
		}
	}
	return start, end, true, true
}
