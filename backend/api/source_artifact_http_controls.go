package api

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
)

const SourceArtifactJSONBodyLimit int64 = 64 * 1024

func (handler *SourceArtifactHandler) allowSourceArtifactAction(c *gin.Context, principal auth.Principal, action sourceArtifactAction) bool {
	allowed, retry := handler.limiter.Allow(principal.TenantID+"\x00"+principal.Subject, action)
	if allowed {
		return true
	}
	seconds := int(math.Ceil(retry.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	handler.writeError(c, http.StatusTooManyRequests, "SOURCE_ARTIFACT_RATE_LIMITED", "source artifact request rate limit exceeded")
	return false
}

func (handler *SourceArtifactHandler) bindCreateSourceArtifactRequest(c *gin.Context, request *createSourceArtifactRequest) bool {
	if c.Request.ContentLength > SourceArtifactJSONBodyLimit {
		handler.writeError(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body exceeds the allowed size")
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, SourceArtifactJSONBodyLimit)
	if err := c.ShouldBindJSON(request); err != nil {
		if isSourceArtifactBodyTooLarge(err) {
			handler.writeError(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body exceeds the allowed size")
			return false
		}
		handler.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return false
	}
	return true
}

func (handler *SourceArtifactHandler) consumeCompleteSourceArtifactBody(c *gin.Context) bool {
	if c.Request.Body == nil {
		return true
	}
	if c.Request.ContentLength > SourceArtifactJSONBodyLimit {
		handler.writeError(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body exceeds the allowed size")
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, SourceArtifactJSONBodyLimit)
	if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
		if isSourceArtifactBodyTooLarge(err) {
			handler.writeError(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body exceeds the allowed size")
			return false
		}
		handler.writeError(c, http.StatusBadRequest, "INVALID_BODY", "request body is invalid")
		return false
	}
	return true
}

func isSourceArtifactBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}
