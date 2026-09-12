package observability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"ray-train-platform-backend/mlflowtracking"
)

const (
	mlflowExternalExperimentSegment = "external"
	mlflowOperationIDTag            = "platform.operation_id"
)

var _ mlflowtracking.Provider = (*MLflowClient)(nil)

// CreateExperiment creates the deterministic upstream experiment for a durable
// platform operation. It checks the deterministic name before and after create
// so retrying an ambiguous upstream result never creates a second experiment.
func (c *MLflowClient) CreateExperiment(ctx context.Context, operationID string) (string, error) {
	if err := c.validateTrackingAdapter(operationID); err != nil {
		return "", err
	}
	upstreamID, found, err := c.FindExperiment(ctx, operationID)
	if err != nil || found {
		return upstreamID, err
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/experiments/create")
	if err != nil {
		return "", trackingUnavailable(err)
	}
	var payload struct {
		ExperimentID string `json:"experiment_id"`
	}
	body := map[string]any{"name": c.externalExperimentName(operationID)}
	if _, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &payload); err != nil {
		if upstreamID, found, findErr := c.FindExperiment(ctx, operationID); findErr == nil && found {
			return upstreamID, nil
		}
		return "", trackingUnavailable(err)
	}
	if strings.TrimSpace(payload.ExperimentID) == "" {
		return "", fmt.Errorf("%w: MLflow experiment create response is incomplete", mlflowtracking.ErrUnavailable)
	}
	return payload.ExperimentID, nil
}

// FindExperiment resolves the deterministic upstream experiment for an
// operation. The experiment name is the reconciliation key because MLflow
// experiment creation is not idempotent.
func (c *MLflowClient) FindExperiment(ctx context.Context, operationID string) (string, bool, error) {
	if err := c.validateTrackingAdapter(operationID); err != nil {
		return "", false, err
	}
	upstreamID, found, err := c.experimentID(ctx, c.externalExperimentName(operationID))
	if err != nil {
		return "", false, trackingUnavailable(err)
	}
	return upstreamID, found, nil
}

// CreateRun creates a run signed by the platform operation. It first reconciles
// by operation tag and repeats that reconciliation after an ambiguous create
// failure before returning an error to the service layer.
func (c *MLflowClient) CreateRun(ctx context.Context, upstreamExperimentID, operationID, name string) (string, error) {
	if err := c.validateTrackingRunRef(upstreamExperimentID, "", operationID); err != nil {
		return "", err
	}
	if upstreamRunID, found, err := c.FindRun(ctx, upstreamExperimentID, operationID); err != nil || found {
		return upstreamRunID, err
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/create")
	if err != nil {
		return "", trackingUnavailable(err)
	}
	var payload struct {
		Run mlflowIntegrationRun `json:"run"`
	}
	body := map[string]any{
		"experiment_id": upstreamExperimentID,
		"run_name":      truncate(strings.TrimSpace(name), 256),
		"start_time":    time.Now().UTC().UnixMilli(),
		"tags": []MLflowKeyValue{
			{Key: mlflowOperationIDTag, Value: operationID},
			{Key: "platform.provenance", Value: externalOperationProvenance(c.ProvenanceKey, operationID)},
		},
	}
	if _, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &payload); err != nil {
		if upstreamRunID, found, findErr := c.FindRun(ctx, upstreamExperimentID, operationID); findErr == nil && found {
			return upstreamRunID, nil
		}
		return "", trackingUnavailable(err)
	}
	if payload.Run.Info.ID == "" {
		return "", fmt.Errorf("%w: MLflow run create response is incomplete", mlflowtracking.ErrUnavailable)
	}
	if !mlflowIntegrationRunID.MatchString(payload.Run.Info.ID) || payload.Run.Info.ExperimentID != upstreamExperimentID || !c.externalOperationTagsMatch(payload.Run, operationID) {
		if upstreamRunID, found, findErr := c.FindRun(ctx, upstreamExperimentID, operationID); findErr == nil && found {
			return upstreamRunID, nil
		}
		return "", fmt.Errorf("%w: MLflow run create response failed platform proof validation", mlflowtracking.ErrUnavailable)
	}
	return payload.Run.Info.ID, nil
}

// FindRun searches for the signed run created by an operation. It returns a
// duplicate error instead of picking arbitrarily if MLflow already contains more
// than one run with valid proof for the same operation.
func (c *MLflowClient) FindRun(ctx context.Context, upstreamExperimentID, operationID string) (string, bool, error) {
	if err := c.validateTrackingRunRef(upstreamExperimentID, "", operationID); err != nil {
		return "", false, err
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/search")
	if err != nil {
		return "", false, trackingUnavailable(err)
	}
	body := map[string]any{
		"experiment_ids": []string{upstreamExperimentID},
		"filter":         "tags.`platform.operation_id` = '" + operationID + "' AND tags.`platform.provenance` = '" + externalOperationProvenance(c.ProvenanceKey, operationID) + "'",
		"order_by":       []string{"attributes.start_time DESC"},
		"max_results":    2,
	}
	var payload struct {
		Runs []mlflowIntegrationRun `json:"runs"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &payload); err != nil {
		return "", false, trackingUnavailable(err)
	}
	matches := make([]string, 0, 1)
	for _, run := range payload.Runs {
		if !mlflowIntegrationRunID.MatchString(run.Info.ID) || run.Info.ExperimentID != upstreamExperimentID || !c.externalOperationTagsMatch(run, operationID) {
			continue
		}
		matches = append(matches, run.Info.ID)
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) > 1 {
		return "", false, fmt.Errorf("%w: MLflow returned duplicate platform runs for operation", mlflowtracking.ErrConflict)
	}
	return matches[0], true, nil
}

// ReadRun returns a sanitized snapshot after verifying that the upstream run is
// still bound to the expected experiment and platform operation proof.
func (c *MLflowClient) ReadRun(ctx context.Context, upstreamExperimentID, upstreamRunID, operationID string) (mlflowtracking.Snapshot, error) {
	run, err := c.trackingRun(ctx, upstreamExperimentID, upstreamRunID, operationID)
	if err != nil {
		return mlflowtracking.Snapshot{}, err
	}
	snapshot := sanitizeTrackingRun(run)
	for _, key := range trackingMetricKeys(snapshot.Latest) {
		points, err := c.metricHistory(ctx, upstreamRunID, key)
		if err != nil {
			return mlflowtracking.Snapshot{}, trackingUnavailable(err)
		}
		snapshot.Series = append(snapshot.Series, mlflowtracking.MetricSeries{Key: key, Points: convertTrackingMetricPoints(points)})
	}
	return snapshot, nil
}

// LogRun verifies the run binding and RUNNING state before forwarding the
// native MLflow batch. MLflow writes are partial on upstream failure.
func (c *MLflowClient) LogRun(ctx context.Context, upstreamExperimentID, upstreamRunID, operationID string, batch mlflowtracking.Batch) error {
	run, err := c.trackingRun(ctx, upstreamExperimentID, upstreamRunID, operationID)
	if err != nil {
		return err
	}
	if run.Info.Status != "RUNNING" {
		return fmt.Errorf("%w: MLflow run is not running", mlflowtracking.ErrConflict)
	}
	converted := convertTrackingBatch(batch)
	if err := converted.Validate(); err != nil {
		return fmt.Errorf("%w: %v", mlflowtracking.ErrInvalid, err)
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/log-batch")
	if err != nil {
		return trackingUnavailable(err)
	}
	body := struct {
		RunID string `json:"run_id"`
		MLflowLogBatch
	}{RunID: upstreamRunID, MLflowLogBatch: converted}
	var result map[string]any
	status, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &result)
	if status == http.StatusBadRequest || status == http.StatusConflict {
		return fmt.Errorf("%w: MLflow rejected the batch", mlflowtracking.ErrConflict)
	}
	if err != nil {
		return trackingUnavailable(err)
	}
	return nil
}

// FinishRun only updates a verified external run to a terminal MLflow status.
func (c *MLflowClient) FinishRun(ctx context.Context, upstreamExperimentID, upstreamRunID, operationID, status string, endTimeMS int64) error {
	if status != "FINISHED" && status != "FAILED" && status != "KILLED" {
		return fmt.Errorf("%w: MLflow terminal status is invalid", mlflowtracking.ErrInvalid)
	}
	run, err := c.trackingRun(ctx, upstreamExperimentID, upstreamRunID, operationID)
	if err != nil {
		return err
	}
	if run.Info.Status == status {
		return nil
	}
	if run.Info.Status != "RUNNING" {
		return fmt.Errorf("%w: MLflow run is not running", mlflowtracking.ErrConflict)
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/update")
	if err != nil {
		return trackingUnavailable(err)
	}
	if endTimeMS < 0 || endTimeMS > 253402300799999 {
		return fmt.Errorf("%w: MLflow end time is invalid", mlflowtracking.ErrInvalid)
	}
	if endTimeMS == 0 {
		endTimeMS = time.Now().UTC().UnixMilli()
	}
	var payload map[string]any
	_, err = c.doJSON(ctx, http.MethodPost, endpoint, map[string]any{
		"run_id":   upstreamRunID,
		"status":   status,
		"end_time": endTimeMS,
	}, &payload)
	if err != nil {
		reconciled, readErr := c.trackingRun(ctx, upstreamExperimentID, upstreamRunID, operationID)
		if readErr == nil && reconciled.Info.Status == status {
			return nil
		}
		return trackingUnavailable(err)
	}
	return nil
}

func (c *MLflowClient) trackingRun(ctx context.Context, upstreamExperimentID, upstreamRunID, operationID string) (mlflowIntegrationRun, error) {
	var zero mlflowIntegrationRun
	if err := c.validateTrackingRunRef(upstreamExperimentID, upstreamRunID, operationID); err != nil {
		return zero, err
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/get")
	if err != nil {
		return zero, trackingUnavailable(err)
	}
	query := endpoint.Query()
	query.Set("run_id", upstreamRunID)
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Run mlflowIntegrationRun `json:"run"`
	}
	status, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &payload)
	if status == http.StatusNotFound {
		return zero, fmt.Errorf("%w: MLflow run was not found", mlflowtracking.ErrNotFound)
	}
	if err != nil {
		return zero, trackingUnavailable(err)
	}
	run := payload.Run
	if !mlflowIntegrationRunID.MatchString(run.Info.ID) || run.Info.ID != upstreamRunID || run.Info.ExperimentID != upstreamExperimentID || !c.externalOperationTagsMatch(run, operationID) {
		return zero, fmt.Errorf("%w: MLflow run was not found", mlflowtracking.ErrNotFound)
	}
	return run, nil
}

func (c *MLflowClient) validateTrackingAdapter(operationID string) error {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" || len(c.ProvenanceKey) < 32 {
		return fmt.Errorf("%w: MLflow tracking adapter is not configured", mlflowtracking.ErrUnavailable)
	}
	if !mlflowIntegrationRunID.MatchString(operationID) {
		return fmt.Errorf("%w: MLflow operation identifier is invalid", mlflowtracking.ErrInvalid)
	}
	prefix := strings.Trim(strings.TrimSpace(c.ExperimentPrefix), "-")
	if prefix != "" && !safeLabelValue(prefix) {
		return fmt.Errorf("%w: MLflow experiment prefix is invalid", mlflowtracking.ErrInvalid)
	}
	return nil
}

func (c *MLflowClient) validateTrackingRunRef(upstreamExperimentID, upstreamRunID, operationID string) error {
	if err := c.validateTrackingAdapter(operationID); err != nil {
		return err
	}
	if !validMLflowExperimentID(upstreamExperimentID) || (upstreamRunID != "" && !mlflowIntegrationRunID.MatchString(upstreamRunID)) {
		return fmt.Errorf("%w: MLflow run was not found", mlflowtracking.ErrNotFound)
	}
	return nil
}

func validMLflowExperimentID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (c *MLflowClient) externalExperimentName(operationID string) string {
	prefix := strings.Trim(strings.TrimSpace(c.ExperimentPrefix), "-")
	if prefix == "" {
		prefix = "raytrain"
	}
	return prefix + "-" + mlflowExternalExperimentSegment + "-" + operationID
}

func (c *MLflowClient) externalOperationTagsMatch(run mlflowIntegrationRun, operationID string) bool {
	tags := make(map[string]string, len(run.Data.Tags))
	for _, tag := range run.Data.Tags {
		if _, exists := tags[tag.Key]; exists {
			return false
		}
		tags[tag.Key] = tag.Value
	}
	return tags[mlflowOperationIDTag] == operationID &&
		hmac.Equal([]byte(tags["platform.provenance"]), []byte(externalOperationProvenance(c.ProvenanceKey, operationID)))
}

func externalOperationProvenance(key []byte, operationID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("raytrain-mlflow-operation:" + operationID))
	return hex.EncodeToString(mac.Sum(nil))
}

func sanitizeTrackingRun(raw mlflowIntegrationRun) mlflowtracking.Snapshot {
	snapshot := mlflowtracking.Snapshot{
		Status:      truncate(raw.Info.Status, 32),
		StartTimeMS: raw.Info.StartTime,
		EndTimeMS:   raw.Info.EndTime,
		Latest:      map[string]float64{},
		Params:      map[string]string{},
		Series:      []mlflowtracking.MetricSeries{},
	}
	for _, metric := range raw.Data.Metrics {
		if len(snapshot.Latest) < maxMLflowMetricKeys && safeMetricKey(metric.Key) && metric.Value.Valid {
			snapshot.Latest[metric.Key] = metric.Value.Value
		}
	}
	for _, param := range raw.Data.Params {
		if len(snapshot.Params) < 100 && validMLflowIntegrationKey(param.Key) {
			snapshot.Params[param.Key] = truncate(param.Value, 1024)
		}
	}
	return snapshot
}

func trackingMetricKeys(latest map[string]float64) []string {
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func convertTrackingMetricPoints(points []MLflowMetricPoint) []mlflowtracking.MetricPoint {
	converted := make([]mlflowtracking.MetricPoint, 0, len(points))
	for _, point := range points {
		converted = append(converted, mlflowtracking.MetricPoint{Value: point.Value, TimestampMS: point.TimestampMS, Step: point.Step})
	}
	return converted
}

func convertTrackingBatch(batch mlflowtracking.Batch) MLflowLogBatch {
	converted := MLflowLogBatch{
		Metrics: make([]MLflowLogMetric, 0, len(batch.Metrics)),
		Params:  make([]MLflowKeyValue, 0, len(batch.Params)),
		Tags:    make([]MLflowKeyValue, 0, len(batch.Tags)),
	}
	for _, metric := range batch.Metrics {
		converted.Metrics = append(converted.Metrics, MLflowLogMetric{Key: metric.Key, Value: metric.Value, Timestamp: metric.Timestamp, Step: metric.Step})
	}
	for _, param := range batch.Params {
		converted.Params = append(converted.Params, MLflowKeyValue{Key: param.Key, Value: param.Value})
	}
	for _, tag := range batch.Tags {
		converted.Tags = append(converted.Tags, MLflowKeyValue{Key: tag.Key, Value: tag.Value})
	}
	return converted
}

func trackingUnavailable(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", mlflowtracking.ErrUnavailable, err)
}
