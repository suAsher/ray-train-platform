package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

type JobRepository interface {
	Create(context.Context, *domain.TrainingJob, string) error
	Get(context.Context, string, string) (*domain.TrainingJob, error)
	List(context.Context, domain.JobFilter) (domain.Page[domain.TrainingJob], error)
	SetDesiredState(context.Context, string, string, domain.DesiredState) error
	EnsureIdentity(context.Context, auth.Principal) error
}

type Handler struct {
	repository           JobRepository
	logs                 LogProvider
	metrics              MetricsProvider
	allowAnonymous       bool
	imageAllowlist       []string
	gitAllowlist         []string
	workspaces           WorkspaceStore
	kubernetes           *k8s.Client
	workspaceImage       string
	rayVersion           string
	serviceAccount       string
	imagePullSecrets     []string
	idcClaim             string
	idcMountPath         string
	clusterQueue         string
	admin                AdminStore
	quota                QuotaStore
	workspacePepper      []byte
	trainingNodeSelector map[string]string
	workspaceUpstream    func(*domain.DevWorkspace) string
	images               ImageStore
	gitCredentials       GitCredentialStore
	newID                func() (string, error)
	submission           *SubmissionService
}

type LogProvider interface {
	QueryJobLogs(context.Context, string, int) ([]observability.LogLine, error)
}

type MetricsProvider interface {
	QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error)
}

type Options struct {
	AllowAnonymous       bool
	Logs                 LogProvider
	Metrics              MetricsProvider
	ImageAllowlist       []string
	GitAllowlist         []string
	Workspaces           WorkspaceStore
	Kubernetes           *k8s.Client
	WorkspaceImage       string
	RayVersion           string
	ServiceAccount       string
	ImagePullSecrets     []string
	IDCClaim             string
	IDCMountPath         string
	KueueClusterQueue    string
	Admin                AdminStore
	Quota                QuotaStore
	WorkspacePepper      []byte
	TrainingNodeSelector map[string]string
	Images               ImageStore
	GitCredentials       GitCredentialStore
}

func NewHandler(repository JobRepository, options Options) *Handler {
	handler := &Handler{repository: repository, logs: options.Logs, metrics: options.Metrics, allowAnonymous: options.AllowAnonymous, imageAllowlist: append([]string(nil), options.ImageAllowlist...), gitAllowlist: append([]string(nil), options.GitAllowlist...), workspaces: options.Workspaces, kubernetes: options.Kubernetes, workspaceImage: options.WorkspaceImage, rayVersion: options.RayVersion, serviceAccount: options.ServiceAccount, imagePullSecrets: append([]string(nil), options.ImagePullSecrets...), idcClaim: options.IDCClaim, idcMountPath: options.IDCMountPath, clusterQueue: options.KueueClusterQueue, admin: options.Admin, quota: options.Quota, workspacePepper: append([]byte(nil), options.WorkspacePepper...), trainingNodeSelector: options.TrainingNodeSelector, images: options.Images, gitCredentials: options.GitCredentials, newID: newJobID}
	handler.submission = NewSubmissionService(repository, SubmissionServiceOptions{
		Images:         handler.images,
		ImageAllowlist: handler.imageAllowlist,
		GitAllowlist:   handler.gitAllowlist,
		ClusterQueue:   handler.clusterQueue,
		EnsureTenantQueue: func(ctx context.Context, namespace, queue, clusterQueue string) error {
			if handler.kubernetes == nil {
				return nil
			}
			return handler.kubernetes.EnsureLocalQueue(ctx, namespace, queue, clusterQueue)
		},
		NewID: func() (string, error) { return handler.newID() },
	})
	return handler
}

type WorkspaceStore interface {
	CreateWorkspace(context.Context, *domain.DevWorkspace, int64) error
	GetWorkspace(context.Context, string, string) (*domain.DevWorkspace, error)
	GetWorkspaceByUser(context.Context, string) (*domain.DevWorkspace, error)
	UpdateWorkspaceState(context.Context, string, string, domain.WorkspaceState) error
}

var _ WorkspaceStore = (*repositories.GormRepository)(nil)

func (h *Handler) RegisterTrainingRoutes(group *gin.RouterGroup) {
	h.registerTrainingRoutes(group)
}

type submitRequest struct {
	Spec domain.JobSpec `json:"spec"`
}

func (h *Handler) submitJob(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed("Engineer") {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	var request submitRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	job, err := h.submission.Submit(c.Request.Context(), SubmissionInput{
		Principal: principal, Spec: request.Spec, Origin: domain.SubmissionOriginPortal,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		h.writeSubmissionError(c, principal, err)
		return
	}
	h.writeSuccess(c, http.StatusAccepted, job)
}

func (h *Handler) writeSubmissionError(c *gin.Context, principal auth.Principal, err error) {
	switch {
	case errors.Is(err, ErrSubmissionQueueNotAllowed):
		h.writeError(c, http.StatusBadRequest, "QUEUE_NOT_ALLOWED", "jobs may only use the authenticated tenant queue")
	case errors.Is(err, ErrSubmissionInvalidJobSpec):
		h.writeError(c, http.StatusBadRequest, "INVALID_JOB_SPEC", "training job spec is invalid")
	case errors.Is(err, ErrSubmissionImageNotAllowed):
		h.writeError(c, http.StatusBadRequest, "IMAGE_NOT_ALLOWED", "the requested image is not in the platform allowlist")
	case errors.Is(err, ErrSubmissionGitNotAllowed):
		h.writeError(c, http.StatusBadRequest, "GIT_SOURCE_NOT_ALLOWED", "the requested Git host is not in the platform allowlist")
	case errors.Is(err, ErrSubmissionArtifactNotFound):
		h.writeError(c, http.StatusNotFound, "SOURCE_ARTIFACT_NOT_FOUND", "source artifact was not found")
	case errors.Is(err, ErrSubmissionArtifactNotReady):
		h.writeError(c, http.StatusConflict, "SOURCE_ARTIFACT_NOT_READY", "source artifact must be READY before submission")
	case errors.Is(err, ErrSubmissionArtifactInvalid):
		h.writeError(c, http.StatusConflict, "SOURCE_ARTIFACT_INVALID", "source artifact is invalid")
	case errors.Is(err, ErrSubmissionQueueProvision):
		h.writeError(c, http.StatusBadGateway, "QUEUE_PROVISION_FAILED", "could not ensure the tenant LocalQueue")
	case errors.Is(err, ErrSubmissionIdentityPersist):
		h.writeError(c, http.StatusInternalServerError, "IDENTITY_PERSIST_FAILED", "could not persist authenticated identity")
	case errors.Is(err, ErrSubmissionIDGeneration):
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate job id")
	default:
		var quotaErr *repositories.GPUQuotaExceededError
		if errors.As(err, &quotaErr) {
			h.writeError(c, http.StatusConflict, "GPU_QUOTA_EXCEEDED", "the tenant GPU quota does not have enough capacity for this job")
			return
		}
		var conflict *repositories.IdempotencyConflictError
		if errors.As(err, &conflict) {
			existing, getErr := h.repository.Get(c.Request.Context(), principal.TenantID, conflict.JobID)
			if getErr == nil {
				h.writeSuccess(c, http.StatusOK, existing)
				return
			}
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			h.writeError(c, http.StatusConflict, "DUPLICATE_JOB", "a job with this name already exists")
			return
		}
		h.writeError(c, http.StatusInternalServerError, "JOB_CREATE_FAILED", "could not create training job")
	}
}

func (h *Handler) listJobs(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	page, err := h.repository.List(c.Request.Context(), domain.JobFilter{TenantID: principal.TenantID, Status: domain.State(c.Query("status")), Keyword: c.Query("keyword"), Limit: limit, Offset: offset})
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "JOB_LIST_FAILED", "could not list training jobs")
		return
	}
	h.writeSuccess(c, http.StatusOK, page)
}

func (h *Handler) getJob(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, err := h.repository.Get(c.Request.Context(), principal.TenantID, c.Param("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		h.writeError(c, status, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	h.writeSuccess(c, http.StatusOK, job)
}

func (h *Handler) cancelJob(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed("Engineer") {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	if err := h.repository.SetDesiredState(c.Request.Context(), principal.TenantID, c.Param("id"), domain.DesiredCanceled); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		h.writeError(c, status, "JOB_CANCEL_FAILED", "could not request job cancellation")
		return
	}
	h.writeSuccess(c, http.StatusAccepted, map[string]string{"id": c.Param("id"), "desiredState": string(domain.DesiredCanceled)})
}

func (h *Handler) getJobLogs(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.logs == nil {
		h.writeError(c, http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "log service is not configured")
		return
	}
	job, err := h.repository.Get(c.Request.Context(), principal.TenantID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	logs, err := h.logs.QueryJobLogs(c.Request.Context(), job.ID, limit)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "LOG_QUERY_FAILED", "could not query job logs")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]any{"jobId": job.ID, "items": logs})
}

func (h *Handler) getJobMetrics(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.metrics == nil {
		h.writeError(c, http.StatusServiceUnavailable, "METRICS_UNAVAILABLE", "metrics service is not configured")
		return
	}
	job, err := h.repository.Get(c.Request.Context(), principal.TenantID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	metrics, err := h.metrics.QueryJobMetrics(c.Request.Context(), job.ID, 24*time.Hour)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "METRICS_QUERY_FAILED", "could not query job metrics")
		return
	}
	h.writeSuccess(c, http.StatusOK, metrics)
}

func (h *Handler) principal(c *gin.Context) (auth.Principal, bool) {
	if principal, ok := auth.PrincipalFromGin(c); ok {
		return principal, true
	}
	if !h.allowAnonymous {
		return auth.Principal{}, false
	}
	return auth.DemoPrincipal(), true
}

func (h *Handler) writeSuccess(c *gin.Context, status int, data any) {
	c.JSON(status, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), data))
}

func (h *Handler) writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")), code, message))
}

func newJobID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("read random job id: %w", err)
	}
	return "job-" + hex.EncodeToString(bytes), nil
}

func tenantQueue(tenantID string) string {
	return sanitizeDNS(tenantID) + "-gpu"
}

func sanitizeDNS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		result = "default"
	}
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}

func matchesAllowlist(value string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed != "" && (value == allowed || strings.HasPrefix(value, allowed+"@")) {
			return true
		}
	}
	return false
}

func matchesGitAllowlist(rawURL string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(strings.ToLower(allowed))
		if allowed != "" && (parsed.Hostname() == allowed || strings.HasSuffix(parsed.Hostname(), "."+allowed)) {
			return true
		}
	}
	return false
}
