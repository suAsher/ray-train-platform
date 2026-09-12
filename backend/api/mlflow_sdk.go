package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

// RegisterMLflowSDKRoutes is an explicit Tracking protocol subset. Resource IDs
// are platform IDs; clients precreate resources through the idempotent REST API.
// No wildcard proxy or artifact location is exposed by this adapter.
func (h *Handler) RegisterMLflowSDKRoutes(v1 *gin.RouterGroup) {
	h.mlflowSDKRegistered = true
	g := v1.Group("/mlflow-tracking/api/2.0/mlflow")
	limiter := newFixedWindowSourceArtifactLimiter(60, 120, 10000, time.Now)
	g.GET("/runs/get", h.mlflowSDKGuard(limiter, false), h.getMLflowSDKRun)
	for _, route := range []string{"log-batch", "log-metric", "log-parameter", "set-tag", "update"} {
		g.POST("/runs/"+route, h.mlflowSDKGuard(limiter, true), h.writeMLflowSDKRun)
	}
}

func (h *Handler) mlflowSDKGuard(limiter SourceArtifactLimiter, write bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromGin(c)
		if !ok {
			sdkError(c, 401, "UNAUTHENTICATED", "authentication is required")
			return
		}
		if p.AuthType != auth.AuthTypePAT || !p.HasScope(domain.PATScopeExperimentsRead) || (write && !p.HasScope(domain.PATScopeExperimentsWrite)) {
			sdkError(c, 403, "PERMISSION_DENIED", "a PAT with explicit external experiment scopes is required")
			return
		}
		action := sourceArtifactActionComplete
		if write {
			action = sourceArtifactActionCreate
		}
		if allowed, _ := limiter.Allow(p.TenantID+"\x00"+p.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			sdkError(c, 429, "REQUEST_LIMIT_EXCEEDED", "request rate limit exceeded")
			return
		}
		if h.mlflowTracking == nil {
			sdkError(c, 503, "TEMPORARILY_UNAVAILABLE", "external tracking is unavailable")
			return
		}
		c.Header("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func sdkError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error_code": code, "message": message})
}

func sdkServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mlflowtracking.ErrInvalid), errors.Is(err, observability.ErrMLflowBatchInvalid):
		sdkError(c, 400, "INVALID_PARAMETER_VALUE", "invalid tracking request")
	case errors.Is(err, mlflowtracking.ErrNotFound):
		sdkError(c, 404, "RESOURCE_DOES_NOT_EXIST", "external run was not found")
	case errors.Is(err, mlflowtracking.ErrConflict), errors.Is(err, mlflowtracking.ErrBusy):
		sdkError(c, 409, "INVALID_STATE", "run state conflicts with the request")
	case errors.Is(err, mlflowtracking.ErrPending):
		sdkError(c, 409, "INVALID_STATE", "operation is pending; read its state before retrying")
	default:
		sdkError(c, 503, "TEMPORARILY_UNAVAILABLE", "tracking outcome is uncertain; verify the run before retrying writes")
	}
}

func sdkActor(c *gin.Context) mlflowtracking.Actor {
	p, _ := auth.PrincipalFromGin(c)
	return mlflowtracking.Actor{TenantID: p.TenantID, UserID: p.Subject}
}

func (h *Handler) getMLflowSDKRun(c *gin.Context) {
	id := c.Query("run_id")
	if !mlflowDashboardRunIDPattern.MatchString(id) || len(c.Request.URL.Query()["run_id"]) != 1 {
		sdkServiceError(c, mlflowtracking.ErrInvalid)
		return
	}
	detail, err := h.mlflowTracking.GetRun(c.Request.Context(), sdkActor(c), id)
	if err != nil {
		sdkServiceError(c, err)
		return
	}
	if detail.Run.ID != id {
		sdkServiceError(c, mlflowtracking.ErrNotFound)
		return
	}
	if detail.Run.State != "RUNNING" && detail.Run.State != "FINISHED" && detail.Run.State != "FAILED" && detail.Run.State != "KILLED" {
		sdkServiceError(c, mlflowtracking.ErrPending)
		return
	}
	c.JSON(200, gin.H{"run": gin.H{"info": sdkRunInfo(detail.Run), "data": sdkRunData(detail)}})
}

func sdkRunData(detail mlflowtracking.RunDetail) gin.H {
	metricKeys := make([]string, 0, len(detail.LatestMetrics))
	for key := range detail.LatestMetrics {
		metricKeys = append(metricKeys, key)
	}
	sort.Strings(metricKeys)
	metrics := make([]gin.H, 0, len(metricKeys))
	for _, key := range metricKeys {
		point := detail.LatestMetrics[key]
		metrics = append(metrics, gin.H{"key": key, "value": point.Value, "timestamp": point.TimestampMS, "step": point.Step})
	}
	params := make([]gin.H, 0, len(detail.Params))
	for key, value := range detail.Params {
		params = append(params, gin.H{"key": key, "value": value})
	}
	sort.Slice(params, func(i, j int) bool { return params[i]["key"].(string) < params[j]["key"].(string) })
	tagKeys := make([]string, 0, len(detail.Tags))
	for key, value := range detail.Tags {
		if mlflowtracking.ReadableUserTag(mlflowtracking.Pair{Key: key, Value: value}) {
			tagKeys = append(tagKeys, key)
		}
	}
	sort.Strings(tagKeys)
	if len(tagKeys) > 100 {
		tagKeys = tagKeys[:100]
	}
	tags := make([]gin.H, 0, len(tagKeys))
	for _, key := range tagKeys {
		tags = append(tags, gin.H{"key": key, "value": detail.Tags[key]})
	}
	return gin.H{"metrics": metrics, "params": params, "tags": tags}
}

func sdkRunInfo(run mlflowtracking.Run) gin.H {
	return gin.H{"run_id": run.ID, "run_uuid": run.ID, "experiment_id": run.ExperimentID, "run_name": run.Name, "status": run.State, "start_time": run.StartTimeMS, "end_time": run.EndTimeMS, "lifecycle_stage": "active", "artifact_uri": "raytrain-disabled:/" + run.ID}
}

type mlflowSDKWriteRequest struct {
	RunID     string                          `json:"run_id"`
	RunUUID   string                          `json:"run_uuid,omitempty"`
	Metrics   []observability.MLflowLogMetric `json:"metrics,omitempty"`
	Params    []observability.MLflowKeyValue  `json:"params,omitempty"`
	Tags      []observability.MLflowKeyValue  `json:"tags,omitempty"`
	Key       string                          `json:"key,omitempty"`
	Value     json.RawMessage                 `json:"value,omitempty"`
	Timestamp *int64                          `json:"timestamp,omitempty"`
	Step      *int64                          `json:"step,omitempty"`
	Status    string                          `json:"status,omitempty"`
	EndTime   *int64                          `json:"end_time,omitempty"`
}

type mlflowTrackingTimedFinisher interface {
	FinishRunAt(context.Context, mlflowtracking.Actor, string, string, int64) (mlflowtracking.Run, error)
}

func decodeMLflowSDKBody(reader io.Reader, target *mlflowSDKWriteRequest) error {
	body, err := io.ReadAll(io.LimitReader(reader, 256*1024+1))
	if err != nil || len(body) > 256*1024 {
		return mlflowtracking.ErrInvalid
	}
	if err := checkMLflowJSONValue(json.NewDecoder(bytes.NewReader(body)), 0); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if !mlflowDashboardRunIDPattern.MatchString(target.RunID) {
		return mlflowtracking.ErrInvalid
	}
	if target.RunUUID != "" && target.RunUUID != target.RunID {
		return mlflowtracking.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return mlflowtracking.ErrInvalid
	}
	return nil
}

func (h *Handler) writeMLflowSDKRun(c *gin.Context) {
	var req mlflowSDKWriteRequest
	if err := decodeMLflowSDKBody(c.Request.Body, &req); err != nil {
		sdkServiceError(c, mlflowtracking.ErrInvalid)
		return
	}
	operation := path.Base(c.FullPath())
	// FullPath may be mounted under a test or service prefix; only the final
	// registered segment controls dispatch, never a client supplied URL.
	if operation == "update" {
		if req.Status != "FINISHED" && req.Status != "FAILED" && req.Status != "KILLED" {
			sdkServiceError(c, mlflowtracking.ErrInvalid)
			return
		}
		if req.Key != "" || len(req.Value) > 0 || len(req.Metrics)+len(req.Params)+len(req.Tags) > 0 || req.Timestamp != nil || req.Step != nil || (req.EndTime != nil && (*req.EndTime < 0 || *req.EndTime > 253402300799999)) {
			sdkServiceError(c, mlflowtracking.ErrInvalid)
			return
		}
		var run mlflowtracking.Run
		err := h.sdkAuditWrite(c, func() error {
			var err error
			if req.EndTime != nil {
				timed, ok := h.mlflowTracking.(mlflowTrackingTimedFinisher)
				if !ok {
					return mlflowtracking.ErrUnavailable
				}
				run, err = timed.FinishRunAt(c.Request.Context(), sdkActor(c), req.RunID, req.Status, *req.EndTime)
			} else {
				run, err = h.mlflowTracking.FinishRun(c.Request.Context(), sdkActor(c), req.RunID, req.Status)
			}
			return err
		})
		if err != nil {
			sdkServiceError(c, err)
			return
		}
		c.JSON(200, gin.H{"run_info": sdkRunInfo(run)})
		return
	}
	batch, err := req.logBatch(operation)
	if err != nil {
		sdkServiceError(c, mlflowtracking.ErrInvalid)
		return
	}
	var input mlflowtracking.Batch
	encoded, _ := json.Marshal(batch)
	if err = json.Unmarshal(encoded, &input); err != nil {
		sdkServiceError(c, mlflowtracking.ErrInvalid)
		return
	}
	err = h.sdkAuditWrite(c, func() error { return h.mlflowTracking.LogRun(c.Request.Context(), sdkActor(c), req.RunID, input) })
	if err != nil {
		sdkServiceError(c, err)
		return
	}
	c.JSON(200, gin.H{})
}

func (req mlflowSDKWriteRequest) logBatch(operation string) (observability.MLflowLogBatch, error) {
	batch := observability.MLflowLogBatch{Metrics: req.Metrics, Params: req.Params, Tags: req.Tags}
	if req.Status != "" || req.EndTime != nil {
		return batch, mlflowtracking.ErrInvalid
	}
	if operation == "log-batch" {
		if req.Key != "" || len(req.Value) > 0 || req.Timestamp != nil || req.Step != nil {
			return batch, mlflowtracking.ErrInvalid
		}
	} else {
		if len(batch.Metrics)+len(batch.Params)+len(batch.Tags) > 0 {
			return batch, mlflowtracking.ErrInvalid
		}
		switch operation {
		case "log-metric":
			var value *float64
			if json.Unmarshal(req.Value, &value) != nil {
				return batch, mlflowtracking.ErrInvalid
			}
			batch.Metrics = []observability.MLflowLogMetric{{Key: req.Key, Value: value, Timestamp: req.Timestamp, Step: req.Step}}
		case "log-parameter", "set-tag":
			var value *string
			if req.Timestamp != nil || req.Step != nil || json.Unmarshal(req.Value, &value) != nil || value == nil {
				return batch, mlflowtracking.ErrInvalid
			}
			pair := observability.MLflowKeyValue{Key: req.Key, Value: *value}
			if operation == "set-tag" {
				batch.Tags = []observability.MLflowKeyValue{pair}
			} else {
				batch.Params = []observability.MLflowKeyValue{pair}
			}
		default:
			return batch, mlflowtracking.ErrInvalid
		}
	}
	return batch, batch.Validate()
}

func (h *Handler) sdkAuditWrite(c *gin.Context, fn func() error) error {
	p, _ := auth.PrincipalFromGin(c)
	action := repositories.MLflowAuditTrackingRunLogBatch
	if path.Base(c.FullPath()) == "update" {
		action = repositories.MLflowAuditTrackingRunFinish
	}
	_, err := h.auditMLflowTrackingWrite(c.Request.Context(), p, http.MethodPost, c.Request.URL.Path, c.GetHeader("X-Request-ID"), action, func(context.Context) (int, error) {
		err := fn()
		status, _, _ := mlflowTrackingErrorDetails(err)
		return status, err
	})
	return err
}
