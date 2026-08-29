package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

const maxTrainingEventBodyBytes int64 = 32 << 10

var trainingEventJobIDPattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)

type trainingEventStore interface {
	RecordTrainingEvent(context.Context, string, []byte, domain.TrainingEvent, time.Time) (domain.TrainingEventResult, error)
}

// RegisterTrainingEventRoutes mounts the worker callback outside the normal
// user middleware. It authenticates exclusively with the job-scoped token.
func (h *Handler) RegisterTrainingEventRoutes(group *gin.RouterGroup) {
	group.POST("/jobs/:id/train-events", h.recordTrainingEvent)
}

func (h *Handler) recordTrainingEvent(c *gin.Context) {
	store, ok := h.repository.(trainingEventStore)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "TRAIN_EVENT_UNAVAILABLE", "managed training events are not configured")
		return
	}
	jobID := c.Param("id")
	if !trainingEventJobIDPattern.MatchString(jobID) {
		h.writeError(c, http.StatusBadRequest, "TRAIN_EVENT_INVALID", "training job ID is invalid")
		return
	}
	token, ok := trainingEventBearer(c.GetHeader("Authorization"))
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "training event authentication failed")
		return
	}
	if c.Request.ContentLength > maxTrainingEventBodyBytes {
		h.writeError(c, http.StatusRequestEntityTooLarge, "TRAIN_EVENT_TOO_LARGE", "training event request is too large")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTrainingEventBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var event domain.TrainingEvent
	if err := decoder.Decode(&event); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(c, http.StatusRequestEntityTooLarge, "TRAIN_EVENT_TOO_LARGE", "training event request is too large")
			return
		}
		h.writeError(c, http.StatusBadRequest, "TRAIN_EVENT_INVALID", "training event request is invalid")
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		h.writeError(c, http.StatusBadRequest, "TRAIN_EVENT_INVALID", "training event request is invalid")
		return
	}
	if err := event.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "TRAIN_EVENT_INVALID", err.Error())
		return
	}
	result, err := store.RecordTrainingEvent(c.Request.Context(), jobID, token, event, time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, repositories.ErrTrainingEventUnauthorized):
			h.writeError(c, http.StatusUnauthorized, "INVALID_AUTHENTICATION", "training event authentication failed")
		case errors.Is(err, repositories.ErrTrainingEventInvalid):
			h.writeError(c, http.StatusBadRequest, "TRAIN_EVENT_INVALID", "training event request is invalid")
		case errors.Is(err, repositories.ErrTrainingEventRateLimited):
			c.Header("Retry-After", "60")
			h.writeError(c, http.StatusTooManyRequests, "TRAIN_EVENT_RATE_LIMITED", "training event rate limit exceeded")
		default:
			h.writeError(c, http.StatusInternalServerError, "TRAIN_EVENT_FAILED", "training event could not be persisted")
		}
		return
	}
	h.writeSuccess(c, http.StatusOK, result)
}

func trainingEventBearer(header string) ([]byte, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.ContainsAny(header, "\r\n") {
		return nil, false
	}
	encoded := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if encoded == "" || strings.Contains(encoded, " ") {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return raw, err == nil && len(raw) == 32
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values are not allowed")
}
