package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

const (
	mlflowTrackingBodyLimit     = 64 * 1024
	mlflowTrackingDefaultLimit  = 50
	mlflowTrackingMaxLimit      = 100
	mlflowTrackingMaxCursorSize = 4096
	mlflowTrackingMaxNameSize   = 128
)

var (
	errMLflowTrackingAuditUnavailable = errors.New("MLflow tracking audit is unavailable")
	errMLflowTrackingAuditIncomplete  = errors.New("MLflow tracking audit completion failed")
)

type mlflowTrackingService interface {
	CreateExperiment(context.Context, mlflowtracking.Actor, string, string) (mlflowtracking.Experiment, error)
	CreateRun(context.Context, mlflowtracking.Actor, string, string, string) (mlflowtracking.Run, error)
	ListExperiments(context.Context, mlflowtracking.Actor, int, string) (mlflowtracking.ExperimentPage, error)
	ListRuns(context.Context, mlflowtracking.Actor, string, int, string) (mlflowtracking.RunPage, error)
	GetRun(context.Context, mlflowtracking.Actor, string) (mlflowtracking.RunDetail, error)
	LogRun(context.Context, mlflowtracking.Actor, string, mlflowtracking.Batch) error
	FinishRun(context.Context, mlflowtracking.Actor, string, string) (mlflowtracking.Run, error)
}

type mlflowTrackingNameRequest struct {
	Name string `json:"name"`
}

type mlflowTrackingFinishRequest struct {
	Status string `json:"status"`
}

type mlflowTrackingCapabilities struct {
	Available                bool                             `json:"available"`
	Read                     bool                             `json:"read"`
	Write                    bool                             `json:"write"`
	SDKCompatible            bool                             `json:"sdkCompatible"`
	SDKBasePath              string                           `json:"sdkBasePath,omitempty"`
	SDKProtocol              string                           `json:"sdkProtocol"`
	SDKClientVersion         string                           `json:"sdkClientVersion"`
	SDKMethods               []string                         `json:"sdkMethods"`
	SDKRequiresPrecreatedRun bool                             `json:"sdkRequiresPrecreatedRun"`
	Scopes                   mlflowTrackingCapabilityScopes   `json:"scopes"`
	Limits                   mlflowTrackingCapabilityLimits   `json:"limits"`
	Supports                 mlflowTrackingCapabilitySupports `json:"supports"`
}

type mlflowTrackingCapabilityScopes struct {
	Read  string `json:"read"`
	Write string `json:"write"`
}

type mlflowTrackingCapabilityLimits struct {
	MaxPageLimit        int `json:"maxPageLimit"`
	MaxMetricKeys       int `json:"maxMetricKeys"`
	MaxMetricPoints     int `json:"maxMetricPoints"`
	MaxBatchMetrics     int `json:"maxBatchMetrics"`
	MaxBatchParams      int `json:"maxBatchParams"`
	MaxBatchTags        int `json:"maxBatchTags"`
	MaxRequestBodyBytes int `json:"maxRequestBodyBytes"`
}

type mlflowTrackingCapabilitySupports struct {
	ExperimentTracking bool `json:"experimentTracking"`
	ArtifactManagement bool `json:"artifactManagement"`
	ModelRegistry      bool `json:"modelRegistry"`
	ModelEvaluation    bool `json:"modelEvaluation"`
	ModelServing       bool `json:"modelServing"`
	UI                 bool `json:"ui"`
}

func (h *Handler) registerMLflowTrackingRoutes(group *gin.RouterGroup) {
	limiter := newFixedWindowSourceArtifactLimiter(60, 120, 10000, time.Now)
	base := group.Group("/mlflow", auth.RequireScopes(domain.PATScopeExperimentsRead), h.mlflowTrackingGuard(limiter, false))
	base.GET("/capabilities", h.getMLflowTrackingCapabilities)
	base.GET("/experiments", h.listMLflowTrackingExperiments)
	base.GET("/experiments/:experimentId/runs", h.listMLflowTrackingRuns)
	base.GET("/runs/:runId", h.getMLflowTrackingRun)

	write := group.Group("/mlflow", auth.RequireScopes(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite), h.mlflowTrackingGuard(limiter, true))
	write.POST("/experiments", h.createMLflowTrackingExperiment)
	write.POST("/experiments/:experimentId/runs", h.createMLflowTrackingRun)
	write.POST("/runs/:runId/log-batch", h.logMLflowTrackingBatch)
	write.POST("/runs/:runId/finish", h.finishMLflowTrackingRun)
}

func (h *Handler) mlflowTrackingGuard(limiter SourceArtifactLimiter, write bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.PrincipalFromGin(c)
		if !ok {
			h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			c.Abort()
			return
		}
		if write && principal.AuthType != auth.AuthTypePAT {
			h.writeError(c, http.StatusForbidden, "MLFLOW_TRACKING_PAT_REQUIRED", "MLflow tracking writes require a personal access token")
			c.Abort()
			return
		}
		action := sourceArtifactActionComplete
		if write {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(principal.TenantID+"\x00"+principal.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, http.StatusTooManyRequests, "RATE_LIMITED", "MLflow tracking request rate limit exceeded")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func (h *Handler) getMLflowTrackingCapabilities(c *gin.Context) {
	principal, _ := auth.PrincipalFromGin(c)
	available := h.mlflowTracking != nil
	sdkCompatible := available && h.mlflowSDKRegistered
	sdkBasePath := ""
	if sdkCompatible {
		sdkBasePath = "/api/v1/mlflow-tracking"
	}
	h.writeSuccess(c, http.StatusOK, mlflowTrackingCapabilities{
		Available:                available,
		Read:                     available,
		Write:                    available && principal.AuthType == auth.AuthTypePAT && principal.HasScope(domain.PATScopeExperimentsWrite),
		SDKCompatible:            sdkCompatible,
		SDKBasePath:              sdkBasePath,
		SDKProtocol:              "tracking-subset",
		SDKClientVersion:         "3.14.0",
		SDKMethods:               []string{"get_run", "log_batch", "log_metric", "log_param", "set_tag", "set_terminated"},
		SDKRequiresPrecreatedRun: true,
		Scopes: mlflowTrackingCapabilityScopes{
			Read:  domain.PATScopeExperimentsRead,
			Write: domain.PATScopeExperimentsWrite,
		},
		Limits: mlflowTrackingCapabilityLimits{
			MaxPageLimit:        mlflowTrackingMaxLimit,
			MaxMetricKeys:       20,
			MaxMetricPoints:     500,
			MaxBatchMetrics:     100,
			MaxBatchParams:      100,
			MaxBatchTags:        100,
			MaxRequestBodyBytes: 256 * 1024,
		},
		Supports: mlflowTrackingCapabilitySupports{
			ExperimentTracking: available,
			UI:                 available,
		},
	})
}

func (h *Handler) listMLflowTrackingExperiments(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingRead(c)
	if !ok {
		return
	}
	limit, cursor, ok := h.mlflowTrackingPage(c)
	if !ok {
		return
	}
	page, err := service.ListExperiments(c.Request.Context(), actor, limit, cursor)
	if err != nil {
		h.mlflowTrackingError(c, 0, err)
		return
	}
	h.writeSuccess(c, http.StatusOK, page)
}

func (h *Handler) createMLflowTrackingExperiment(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingWrite(c)
	if !ok {
		return
	}
	idempotencyKey, ok := h.mlflowTrackingIdempotencyKey(c)
	if !ok {
		return
	}
	var request mlflowTrackingNameRequest
	if !h.decodeMLflowTrackingJSON(c, &request) {
		return
	}
	name, ok := h.mlflowTrackingName(c, request.Name)
	if !ok {
		return
	}
	var experiment mlflowtracking.Experiment
	status, err := h.auditMLflowTrackingWrite(c.Request.Context(), actorPrincipal(c), http.MethodPost, c.Request.URL.Path, c.GetHeader("X-Request-ID"), repositories.MLflowAuditTrackingExperimentCreate, func(ctx context.Context) (int, error) {
		var createErr error
		experiment, createErr = service.CreateExperiment(ctx, actor, idempotencyKey, name)
		if createErr != nil {
			status, _, _ := mlflowTrackingErrorDetails(createErr)
			return status, createErr
		}
		return http.StatusCreated, nil
	})
	if err != nil {
		h.mlflowTrackingError(c, status, err)
		return
	}
	h.writeSuccess(c, status, experiment)
}

func (h *Handler) listMLflowTrackingRuns(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingRead(c)
	if !ok {
		return
	}
	experimentID, ok := h.mlflowTrackingID(c, "experimentId")
	if !ok {
		return
	}
	limit, cursor, ok := h.mlflowTrackingPage(c)
	if !ok {
		return
	}
	page, err := service.ListRuns(c.Request.Context(), actor, experimentID, limit, cursor)
	if err != nil {
		h.mlflowTrackingError(c, 0, err)
		return
	}
	h.writeSuccess(c, http.StatusOK, page)
}

func (h *Handler) createMLflowTrackingRun(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingWrite(c)
	if !ok {
		return
	}
	experimentID, ok := h.mlflowTrackingID(c, "experimentId")
	if !ok {
		return
	}
	idempotencyKey, ok := h.mlflowTrackingIdempotencyKey(c)
	if !ok {
		return
	}
	var request mlflowTrackingNameRequest
	if !h.decodeMLflowTrackingJSON(c, &request) {
		return
	}
	name, ok := h.mlflowTrackingName(c, request.Name)
	if !ok {
		return
	}
	var run mlflowtracking.Run
	status, err := h.auditMLflowTrackingWrite(c.Request.Context(), actorPrincipal(c), http.MethodPost, c.Request.URL.Path, c.GetHeader("X-Request-ID"), repositories.MLflowAuditTrackingRunCreate, func(ctx context.Context) (int, error) {
		var createErr error
		run, createErr = service.CreateRun(ctx, actor, experimentID, idempotencyKey, name)
		if createErr != nil {
			status, _, _ := mlflowTrackingErrorDetails(createErr)
			return status, createErr
		}
		return http.StatusCreated, nil
	})
	if err != nil {
		h.mlflowTrackingError(c, status, err)
		return
	}
	h.writeSuccess(c, status, run)
}

func (h *Handler) getMLflowTrackingRun(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingRead(c)
	if !ok {
		return
	}
	runID, ok := h.mlflowTrackingID(c, "runId")
	if !ok {
		return
	}
	detail, err := service.GetRun(c.Request.Context(), actor, runID)
	if err != nil {
		h.mlflowTrackingError(c, 0, err)
		return
	}
	h.writeSuccess(c, http.StatusOK, detail)
}

func (h *Handler) logMLflowTrackingBatch(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingWrite(c)
	if !ok {
		return
	}
	runID, ok := h.mlflowTrackingID(c, "runId")
	if !ok {
		return
	}
	batch, ok := h.decodeMLflowIntegrationBatch(c)
	if !ok {
		return
	}
	converted := mlflowTrackingBatch(batch)
	status, err := h.auditMLflowTrackingWrite(c.Request.Context(), actorPrincipal(c), http.MethodPost, c.Request.URL.Path, c.GetHeader("X-Request-ID"), repositories.MLflowAuditTrackingRunLogBatch, func(ctx context.Context) (int, error) {
		logErr := service.LogRun(ctx, actor, runID, converted)
		if logErr != nil {
			status, _, _ := mlflowTrackingErrorDetails(logErr)
			return status, logErr
		}
		return http.StatusOK, nil
	})
	if err != nil {
		h.mlflowTrackingError(c, status, err)
		return
	}
	h.writeSuccess(c, status, gin.H{"runId": runID, "logged": true})
}

func (h *Handler) finishMLflowTrackingRun(c *gin.Context) {
	service, actor, ok := h.mlflowTrackingWrite(c)
	if !ok {
		return
	}
	runID, ok := h.mlflowTrackingID(c, "runId")
	if !ok {
		return
	}
	var request mlflowTrackingFinishRequest
	if !h.decodeMLflowTrackingJSON(c, &request) {
		return
	}
	statusName, ok := h.mlflowTrackingFinishStatus(c, request.Status)
	if !ok {
		return
	}
	var run mlflowtracking.Run
	status, err := h.auditMLflowTrackingWrite(c.Request.Context(), actorPrincipal(c), http.MethodPost, c.Request.URL.Path, c.GetHeader("X-Request-ID"), repositories.MLflowAuditTrackingRunFinish, func(ctx context.Context) (int, error) {
		var finishErr error
		run, finishErr = service.FinishRun(ctx, actor, runID, statusName)
		if finishErr != nil {
			status, _, _ := mlflowTrackingErrorDetails(finishErr)
			return status, finishErr
		}
		return http.StatusOK, nil
	})
	if err != nil {
		h.mlflowTrackingError(c, status, err)
		return
	}
	h.writeSuccess(c, status, run)
}

func (h *Handler) mlflowTrackingRead(c *gin.Context) (mlflowTrackingService, mlflowtracking.Actor, bool) {
	principal, _ := auth.PrincipalFromGin(c)
	if h.mlflowTracking == nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_TRACKING_UNAVAILABLE", "MLflow tracking is not configured")
		return nil, mlflowtracking.Actor{}, false
	}
	return h.mlflowTracking, mlflowtracking.Actor{TenantID: principal.TenantID, UserID: principal.Subject}, true
}

func (h *Handler) mlflowTrackingWrite(c *gin.Context) (mlflowTrackingService, mlflowtracking.Actor, bool) {
	return h.mlflowTrackingRead(c)
}

func (h *Handler) mlflowTrackingPage(c *gin.Context) (int, string, bool) {
	limit := mlflowTrackingDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > mlflowTrackingMaxLimit {
		limit = mlflowTrackingMaxLimit
	}
	cursor := strings.TrimSpace(c.Query("cursor"))
	if len(cursor) > mlflowTrackingMaxCursorSize || hasControl(cursor) {
		h.writeError(c, http.StatusBadRequest, "MLFLOW_TRACKING_INVALID_CURSOR", "tracking cursor is invalid")
		return 0, "", false
	}
	return limit, cursor, true
}

func (h *Handler) mlflowTrackingID(c *gin.Context, name string) (string, bool) {
	id := strings.TrimSpace(c.Param(name))
	if !isMLflowTrackingID(id) {
		h.writeError(c, http.StatusBadRequest, "MLFLOW_TRACKING_INVALID_ID", "tracking identifier is invalid")
		return "", false
	}
	return id, true
}

func (h *Handler) mlflowTrackingName(c *gin.Context, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > mlflowTrackingMaxNameSize || hasControl(value) {
		h.writeError(c, http.StatusBadRequest, "MLFLOW_TRACKING_INVALID_NAME", "tracking name is invalid")
		return "", false
	}
	return value, true
}

func (h *Handler) mlflowTrackingFinishStatus(c *gin.Context, value string) (string, bool) {
	status := strings.ToUpper(strings.TrimSpace(value))
	switch status {
	case "FINISHED", "FAILED", "KILLED":
		return status, true
	default:
		h.writeError(c, http.StatusBadRequest, "MLFLOW_TRACKING_INVALID_STATUS", "tracking run finish status is invalid")
		return "", false
	}
}

func (h *Handler) mlflowTrackingIdempotencyKey(c *gin.Context) (string, bool) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 128 || !isSafeASCIIHeaderValue(key) {
		h.writeError(c, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
		return "", false
	}
	return key, true
}

func (h *Handler) decodeMLflowTrackingJSON(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, mlflowTrackingBodyLimit)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(c, http.StatusRequestEntityTooLarge, "MLFLOW_TRACKING_BODY_TOO_LARGE", "tracking request body is too large")
		} else {
			h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		}
		return false
	}
	check := json.NewDecoder(bytes.NewReader(body))
	if err := checkMLflowJSONValue(check, 0); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body contains invalid or duplicate fields")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body has trailing data")
		return false
	}
	return true
}

func (h *Handler) auditMLflowTrackingWrite(ctx context.Context, principal auth.Principal, method, path, requestID string, action repositories.MLflowAuditAction, fn func(context.Context) (int, error)) (int, error) {
	if h.mlflowDashboardStore == nil {
		return http.StatusServiceUnavailable, errMLflowTrackingAuditUnavailable
	}
	started := time.Now()
	event := repositories.MLflowAuditEvent{Action: action, Principal: principal, Method: method, Path: path, Status: http.StatusProcessing, RequestID: requestID}
	if err := h.mlflowDashboardStore.CreateMLflowAuditLog(ctx, event); err != nil {
		return http.StatusServiceUnavailable, errMLflowTrackingAuditUnavailable
	}
	status, err := fn(ctx)
	if status == 0 {
		if err != nil {
			status, _, _ = mlflowTrackingErrorDetails(err)
		} else {
			status = http.StatusOK
		}
	}
	completion := event
	completion.Status = status
	completion.Duration = time.Since(started)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if auditErr := h.mlflowDashboardStore.CreateMLflowAuditLog(auditCtx, completion); auditErr != nil {
		return http.StatusServiceUnavailable, errMLflowTrackingAuditIncomplete
	}
	return status, err
}

func (h *Handler) mlflowTrackingError(c *gin.Context, status int, err error) {
	if status == 0 {
		status, _, _ = mlflowTrackingErrorDetails(err)
	}
	_, code, message := mlflowTrackingErrorDetails(err)
	h.writeError(c, status, code, message)
}

func mlflowTrackingErrorDetails(err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusOK, "", ""
	case errors.Is(err, errMLflowTrackingAuditUnavailable):
		return http.StatusServiceUnavailable, "MLFLOW_TRACKING_AUDIT_UNAVAILABLE", "MLflow tracking write audit is unavailable"
	case errors.Is(err, errMLflowTrackingAuditIncomplete):
		return http.StatusServiceUnavailable, "MLFLOW_TRACKING_AUDIT_INCOMPLETE", "MLflow may have accepted the request; verify the run before retrying"
	case errors.Is(err, mlflowtracking.ErrInvalid):
		return http.StatusBadRequest, "MLFLOW_TRACKING_INVALID_REQUEST", "MLflow tracking request is invalid"
	case errors.Is(err, mlflowtracking.ErrNotFound):
		return http.StatusNotFound, "MLFLOW_TRACKING_NOT_FOUND", "MLflow tracking record was not found"
	case errors.Is(err, mlflowtracking.ErrConflict):
		return http.StatusConflict, "MLFLOW_TRACKING_CONFLICT", "MLflow tracking state conflicts with this request"
	case errors.Is(err, mlflowtracking.ErrBusy):
		return http.StatusConflict, "MLFLOW_TRACKING_BUSY", "MLflow tracking mutation is already in progress"
	case errors.Is(err, mlflowtracking.ErrPending):
		return http.StatusConflict, "MLFLOW_TRACKING_PENDING", "MLflow tracking operation is still pending"
	case errors.Is(err, mlflowtracking.ErrUnavailable):
		return http.StatusServiceUnavailable, "MLFLOW_TRACKING_UNAVAILABLE", "MLflow tracking is unavailable"
	default:
		return http.StatusBadGateway, "MLFLOW_TRACKING_REQUEST_FAILED", "MLflow tracking request failed"
	}
}

func mlflowTrackingBatch(batch observability.MLflowLogBatch) mlflowtracking.Batch {
	converted := mlflowtracking.Batch{
		Metrics: make([]mlflowtracking.Metric, 0, len(batch.Metrics)),
		Params:  make([]mlflowtracking.Pair, 0, len(batch.Params)),
		Tags:    make([]mlflowtracking.Pair, 0, len(batch.Tags)),
	}
	for _, metric := range batch.Metrics {
		converted.Metrics = append(converted.Metrics, mlflowtracking.Metric{Key: metric.Key, Value: metric.Value, Timestamp: metric.Timestamp, Step: metric.Step})
	}
	for _, param := range batch.Params {
		converted.Params = append(converted.Params, mlflowtracking.Pair{Key: param.Key, Value: param.Value})
	}
	for _, tag := range batch.Tags {
		converted.Tags = append(converted.Tags, mlflowtracking.Pair{Key: tag.Key, Value: tag.Value})
	}
	return converted
}

func actorPrincipal(c *gin.Context) auth.Principal {
	principal, _ := auth.PrincipalFromGin(c)
	return principal
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func isSafeASCIIHeaderValue(value string) bool {
	for _, character := range value {
		if character < 33 || character > 126 {
			return false
		}
	}
	return true
}

func isMLflowTrackingID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
