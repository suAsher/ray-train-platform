package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"
	"ray-train-platform-backend/domain"
	platformk8s "ray-train-platform-backend/k8s"
)

type jobWorkerConnector interface {
	ResolveJobWorker(context.Context, string, string, int) (platformk8s.JobWorkerTarget, error)
	ConnectJobWorker(context.Context, platformk8s.JobWorkerTarget, io.Reader, io.Writer) error
}

func (h *Handler) connectJobWorker(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleEngineer) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	// Worker access is deliberately stricter than job visibility: even a team
	// administrator or SuperAdmin must not enter another user's process.
	if job.UserID != principal.Subject {
		h.writeError(c, http.StatusForbidden, "WORKER_CONNECT_FORBIDDEN", "only the job owner can connect to a training worker")
		return
	}
	if job.ObservedState != domain.StateRunning && job.ObservedState != domain.StateRecovering {
		h.writeError(c, http.StatusConflict, "WORKER_NOT_RUNNING", "the training job has no running worker")
		return
	}
	worker, err := strconv.Atoi(c.DefaultQuery("worker", "0"))
	if err != nil || worker < 0 || worker > 999 {
		h.writeError(c, http.StatusBadRequest, "INVALID_WORKER", "worker must be a zero-based ordinal")
		return
	}
	if h.jobWorkerConnector == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKER_CONNECT_UNAVAILABLE", "worker connection is not configured")
		return
	}
	namespace := strings.TrimSpace(job.KubernetesNS)
	if namespace == "" {
		namespace = "tenant-" + sanitizeDNS(job.TenantID)
	}
	target, err := h.jobWorkerConnector.ResolveJobWorker(c.Request.Context(), namespace, job.ID, worker)
	if errors.Is(err, platformk8s.ErrJobWorkerNotFound) {
		h.writeError(c, http.StatusNotFound, "WORKER_NOT_FOUND", "the selected training worker was not found")
		return
	}
	if errors.Is(err, platformk8s.ErrJobWorkerNotRunning) {
		h.writeError(c, http.StatusConflict, "WORKER_NOT_RUNNING", "the selected training worker is not running")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKER_QUERY_FAILED", "could not resolve the training worker")
		return
	}

	server := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(connection *websocket.Conn) {
			connection.PayloadType = websocket.BinaryFrame
			if connectErr := h.jobWorkerConnector.ConnectJobWorker(c.Request.Context(), target, connection, connection); connectErr != nil {
				_ = websocket.Message.Send(connection, "\r\nWorker connection ended.\r\n")
			}
		},
	}
	server.ServeHTTP(c.Writer, c.Request)
}
