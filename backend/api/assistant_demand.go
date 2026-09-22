package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/repositories"
)

const (
	assistantDemandAuthMessage = "assistant-idle-demand:v1"
	assistantDemandDBTimeout   = time.Second
)

type AssistantDemandStore interface {
	AggregateAssistantDemand(context.Context) (repositories.AssistantDemandSnapshot, error)
}

func (h *Handler) RegisterAssistantDemandInternalRoutes(group *gin.RouterGroup) {
	if h == nil || h.assistantDemand == nil || len(h.assistantDemandAuthKey) == 0 {
		return
	}
	group.GET("/assistant-idle/training-demand", h.assistantIdleTrainingDemand)
	group.GET("/assistant-idle/training-demand/", h.assistantIdleTrainingDemandInvalidPath)
}

func (h *Handler) assistantIdleTrainingDemandInvalidPath(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	h.writeError(c, http.StatusNotFound, "ASSISTANT_DEMAND_NOT_FOUND", "assistant demand route was not found")
}

func (h *Handler) assistantIdleTrainingDemand(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.Request.URL.Path != "/api/v1/internal/assistant-idle/training-demand" {
		h.writeError(c, http.StatusNotFound, "ASSISTANT_DEMAND_NOT_FOUND", "assistant demand route was not found")
		return
	}
	if c.Request.URL.RawQuery != "" {
		h.writeError(c, http.StatusBadRequest, "ASSISTANT_DEMAND_INVALID_QUERY", "assistant demand does not accept query parameters")
		return
	}
	if assistantDemandRequestHasBody(c.Request) {
		h.writeError(c, http.StatusBadRequest, "ASSISTANT_DEMAND_BODY_FORBIDDEN", "assistant demand does not accept a request body")
		return
	}
	if !h.authorizeAssistantDemand(c.GetHeader("Authorization")) {
		h.writeError(c, http.StatusUnauthorized, "ASSISTANT_DEMAND_UNAUTHORIZED", "assistant demand authentication failed")
		return
	}
	demandContext, cancel := context.WithTimeout(c.Request.Context(), assistantDemandDBTimeout)
	defer cancel()
	snapshot, err := h.assistantDemand.AggregateAssistantDemand(demandContext)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "ASSISTANT_DEMAND_UNAVAILABLE", "assistant demand observation is unavailable")
		return
	}
	h.writeSuccess(c, http.StatusOK, snapshot)
}

func (h *Handler) authorizeAssistantDemand(header string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if provided == "" || strings.ContainsAny(provided, " \r\n") {
		return false
	}
	mac := hmac.New(sha256.New, h.assistantDemandAuthKey)
	_, _ = mac.Write([]byte(assistantDemandAuthMessage))
	want := hex.EncodeToString(mac.Sum(nil))
	return len(provided) == len(want) && hmac.Equal([]byte(provided), []byte(want))
}

func assistantDemandRequestHasBody(request *http.Request) bool {
	if request == nil {
		return false
	}
	if request.ContentLength > 0 || request.ContentLength == -1 {
		return true
	}
	if len(request.TransferEncoding) > 0 {
		return true
	}
	return request.Body != nil && request.Body != http.NoBody
}
