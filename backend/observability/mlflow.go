package observability

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

const (
	maxMLflowResponseBytes = 8 * 1024 * 1024
	maxMLflowMetricKeys    = 20
	maxMLflowMetricPoints  = 500
	maxMLflowRunsPerJob    = 1000
)

// MLflowClient is used only by the control plane. Browsers do not connect to
// MLflow directly, which keeps artifact paths and the internal service hidden.
type MLflowClient struct {
	BaseURL          string
	ExperimentPrefix string
	ProvenanceKey    []byte
	HTTPClient       *http.Client
}

type JobExperiment struct {
	ExperimentName string               `json:"experimentName"`
	Run            *ExperimentRun       `json:"run,omitempty"`
	Series         []MLflowMetricSeries `json:"series"`
}

// ExperimentCatalog is a sanitized tenant view. It deliberately omits MLflow
// artifact locations and arbitrary tags because those fields can reveal
// storage topology or bypass the platform's no-download policy.
type ExperimentCatalog struct {
	ExperimentName string                 `json:"experimentName"`
	Runs           []ExperimentRunSummary `json:"runs"`
}

type ExperimentRunSummary struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Status          string             `json:"status"`
	JobID           string             `json:"jobId"`
	SubmitterUserID string             `json:"submitterUserId,omitempty"`
	StartTimeMS     int64              `json:"startTimeMs,omitempty"`
	EndTimeMS       int64              `json:"endTimeMs,omitempty"`
	Latest          map[string]float64 `json:"latest"`
}

type ExperimentRun struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Status      string             `json:"status"`
	StartTimeMS int64              `json:"startTimeMs,omitempty"`
	EndTimeMS   int64              `json:"endTimeMs,omitempty"`
	Latest      map[string]float64 `json:"latest"`
	Params      map[string]string  `json:"params,omitempty"`
}

type MLflowMetricSeries struct {
	Key    string              `json:"key"`
	Points []MLflowMetricPoint `json:"points"`
}

type MLflowMetricPoint struct {
	Value       float64 `json:"value"`
	TimestampMS int64   `json:"timestampMs"`
	Step        int64   `json:"step"`
}

// mlflowMetricValue tolerates MLflow's representation of non-finite metric
// values as JSON strings (for example "NaN"). A single undefined validation
// metric must not make every other metric for the run unavailable.
type mlflowMetricValue struct {
	Value float64
	Valid bool
}

func (v *mlflowMetricValue) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return nil
		}
		raw = text
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	v.Value = value
	v.Valid = true
	return nil
}

func (c *MLflowClient) QueryJobExperiment(ctx context.Context, tenantID, jobID string) (JobExperiment, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return JobExperiment{}, fmt.Errorf("MLflow URL is not configured")
	}
	if len(c.ProvenanceKey) < 32 {
		return JobExperiment{}, fmt.Errorf("MLflow provenance key is not configured")
	}
	if !safeLabelValue(tenantID) || !safeLabelValue(jobID) {
		return JobExperiment{}, fmt.Errorf("tenant or job identifier is invalid")
	}
	prefix := strings.Trim(strings.TrimSpace(c.ExperimentPrefix), "-")
	if prefix == "" {
		prefix = "raytrain"
	}
	if !safeLabelValue(prefix) {
		return JobExperiment{}, fmt.Errorf("MLflow experiment prefix is invalid")
	}
	experimentName := prefix + "-" + tenantID
	result := JobExperiment{ExperimentName: experimentName, Series: []MLflowMetricSeries{}}

	experimentID, found, err := c.experimentID(ctx, experimentName)
	if err != nil || !found {
		return result, err
	}
	run, metricKeys, found, err := c.jobRun(ctx, experimentID, jobID)
	if err != nil || !found {
		return result, err
	}
	result.Run = run
	for _, key := range metricKeys {
		points, err := c.metricHistory(ctx, run.ID, key)
		if err != nil {
			return JobExperiment{}, err
		}
		result.Series = append(result.Series, MLflowMetricSeries{Key: key, Points: points})
	}
	return result, nil
}

// ListTenantExperiments returns recent runs in the authenticated tenant's
// experiment. A non-empty subject restricts the result to that submitter;
// callers may pass an empty subject only after authorising a tenant admin.
func (c *MLflowClient) ListTenantExperiments(ctx context.Context, tenantID, subject string, limit int) (ExperimentCatalog, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return ExperimentCatalog{}, fmt.Errorf("MLflow URL is not configured")
	}
	if len(c.ProvenanceKey) < 32 {
		return ExperimentCatalog{}, fmt.Errorf("MLflow provenance key is not configured")
	}
	if !safeLabelValue(tenantID) || (subject != "" && !safeLabelValue(subject)) {
		return ExperimentCatalog{}, fmt.Errorf("tenant or subject identifier is invalid")
	}
	if limit < 1 || limit > 100 {
		return ExperimentCatalog{}, fmt.Errorf("experiment limit must be between 1 and 100")
	}
	prefix := strings.Trim(strings.TrimSpace(c.ExperimentPrefix), "-")
	if prefix == "" {
		prefix = "raytrain"
	}
	if !safeLabelValue(prefix) {
		return ExperimentCatalog{}, fmt.Errorf("MLflow experiment prefix is invalid")
	}
	experimentName := prefix + "-" + tenantID
	result := ExperimentCatalog{ExperimentName: experimentName, Runs: []ExperimentRunSummary{}}
	experimentID, found, err := c.experimentID(ctx, experimentName)
	if err != nil || !found {
		return result, err
	}

	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/search")
	if err != nil {
		return ExperimentCatalog{}, err
	}
	body := map[string]any{
		"experiment_ids": []string{experimentID},
		"order_by":       []string{"attributes.start_time DESC"},
		"max_results":    limit,
	}
	if subject != "" {
		body["filter"] = "tags.`platform.submitter_user_id` = '" + subject + "'"
	}
	var payload struct {
		Runs []struct {
			Info struct {
				ID        string `json:"run_id"`
				Name      string `json:"run_name"`
				Status    string `json:"status"`
				StartTime int64  `json:"start_time"`
				EndTime   int64  `json:"end_time"`
			} `json:"info"`
			Data struct {
				Metrics []struct {
					Key   string            `json:"key"`
					Value mlflowMetricValue `json:"value"`
				} `json:"metrics"`
				Tags []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"tags"`
			} `json:"data"`
		} `json:"runs"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &payload); err != nil {
		return ExperimentCatalog{}, err
	}
	for _, raw := range payload.Runs {
		if raw.Info.ID == "" {
			continue
		}
		run := ExperimentRunSummary{
			ID: raw.Info.ID, Name: truncate(raw.Info.Name, 256), Status: truncate(raw.Info.Status, 32),
			StartTimeMS: raw.Info.StartTime, EndTimeMS: raw.Info.EndTime,
			Latest: map[string]float64{},
		}
		for _, metric := range raw.Data.Metrics {
			if len(run.Latest) >= maxMLflowMetricKeys || !safeMetricKey(metric.Key) || !metric.Value.Valid {
				continue
			}
			run.Latest[metric.Key] = metric.Value.Value
		}
		provenance := ""
		for _, tag := range raw.Data.Tags {
			switch tag.Key {
			case "platform.job_id":
				if safeLabelValue(tag.Value) {
					run.JobID = tag.Value
				}
			case "platform.submitter_user_id":
				if safeLabelValue(tag.Value) {
					run.SubmitterUserID = tag.Value
				}
			case "platform.provenance":
				provenance = tag.Value
			}
		}
		// Runs not created by the platform cannot be linked to an authorised
		// training job and are intentionally hidden from the Portal catalog.
		if run.JobID == "" || !hmac.Equal([]byte(provenance), []byte(mlflowProvenanceTag(c.ProvenanceKey, run.JobID))) {
			continue
		}
		result.Runs = append(result.Runs, run)
	}
	return result, nil
}

// FinalizeJobRuns closes every still-running MLflow run that is cryptographically
// bound to a terminal platform job. It is safe to retry: the search only returns
// RUNNING runs, and MLflow's run update is idempotent for a terminal status.
func (c *MLflowClient) FinalizeJobRuns(ctx context.Context, tenantID, jobID string, state domain.State, finishedAt time.Time) error {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("MLflow URL is not configured")
	}
	if len(c.ProvenanceKey) < 32 {
		return fmt.Errorf("MLflow provenance key is not configured")
	}
	if !safeLabelValue(tenantID) || !safeLabelValue(jobID) {
		return fmt.Errorf("tenant or job identifier is invalid")
	}
	status, err := mlflowTerminalStatus(state)
	if err != nil {
		return err
	}
	prefix := strings.Trim(strings.TrimSpace(c.ExperimentPrefix), "-")
	if prefix == "" {
		prefix = "raytrain"
	}
	if !safeLabelValue(prefix) {
		return fmt.Errorf("MLflow experiment prefix is invalid")
	}
	experimentID, found, err := c.experimentID(ctx, prefix+"-"+tenantID)
	if err != nil || !found {
		return err
	}
	runIDs, err := c.runningJobRunIDs(ctx, experimentID, jobID)
	if err != nil {
		return err
	}
	endTime := finishedAt.UTC().UnixMilli()
	if endTime <= 0 {
		endTime = time.Now().UTC().UnixMilli()
	}
	for _, runID := range runIDs {
		endpoint, endpointErr := c.endpoint("/api/2.0/mlflow/runs/update")
		if endpointErr != nil {
			return endpointErr
		}
		var payload map[string]any
		if _, updateErr := c.doJSON(ctx, http.MethodPost, endpoint, map[string]any{
			"run_id": runID, "status": status, "end_time": endTime,
		}, &payload); updateErr != nil {
			return fmt.Errorf("finalize MLflow run %s: %w", runID, updateErr)
		}
	}
	return nil
}

func mlflowTerminalStatus(state domain.State) (string, error) {
	switch state {
	case domain.StateSucceeded:
		return "FINISHED", nil
	case domain.StateFailed, domain.StateTimedOut:
		return "FAILED", nil
	case domain.StateCanceled:
		return "KILLED", nil
	default:
		return "", fmt.Errorf("job state %s is not terminal", state)
	}
}

func (c *MLflowClient) runningJobRunIDs(ctx context.Context, experimentID, jobID string) ([]string, error) {
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/search")
	if err != nil {
		return nil, err
	}
	filter := "attributes.status = 'RUNNING' AND tags.`platform.job_id` = '" + jobID + "' AND tags.`platform.provenance` = '" + mlflowProvenanceTag(c.ProvenanceKey, jobID) + "'"
	runIDs := make([]string, 0)
	pageToken := ""
	seenTokens := map[string]struct{}{}
	for {
		body := map[string]any{"experiment_ids": []string{experimentID}, "filter": filter, "max_results": 100}
		if pageToken != "" {
			body["page_token"] = pageToken
		}
		var payload struct {
			Runs []struct {
				Info struct {
					ID string `json:"run_id"`
				} `json:"info"`
			} `json:"runs"`
			NextPageToken string `json:"next_page_token"`
		}
		if _, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &payload); err != nil {
			return nil, err
		}
		for _, run := range payload.Runs {
			if run.Info.ID != "" {
				runIDs = append(runIDs, run.Info.ID)
				if len(runIDs) > maxMLflowRunsPerJob {
					return nil, fmt.Errorf("MLflow job has more than %d running runs", maxMLflowRunsPerJob)
				}
			}
		}
		pageToken = strings.TrimSpace(payload.NextPageToken)
		if pageToken == "" {
			return runIDs, nil
		}
		if _, exists := seenTokens[pageToken]; exists {
			return nil, fmt.Errorf("MLflow returned a repeated page token")
		}
		seenTokens[pageToken] = struct{}{}
	}
}

func (c *MLflowClient) experimentID(ctx context.Context, name string) (string, bool, error) {
	endpoint, err := c.endpoint("/api/2.0/mlflow/experiments/get-by-name")
	if err != nil {
		return "", false, err
	}
	query := endpoint.Query()
	query.Set("experiment_name", name)
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Experiment struct {
			ID string `json:"experiment_id"`
		} `json:"experiment"`
	}
	status, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &payload)
	if status == http.StatusNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if payload.Experiment.ID == "" {
		return "", false, fmt.Errorf("MLflow experiment response is incomplete")
	}
	return payload.Experiment.ID, true, nil
}

func (c *MLflowClient) jobRun(ctx context.Context, experimentID, jobID string) (*ExperimentRun, []string, bool, error) {
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/search")
	if err != nil {
		return nil, nil, false, err
	}
	body := map[string]any{
		"experiment_ids": []string{experimentID},
		"filter":         "tags.`platform.job_id` = '" + jobID + "' AND tags.`platform.provenance` = '" + mlflowProvenanceTag(c.ProvenanceKey, jobID) + "'",
		"order_by":       []string{"attributes.start_time DESC"},
		"max_results":    1,
	}
	var payload struct {
		Runs []struct {
			Info struct {
				ID        string `json:"run_id"`
				Name      string `json:"run_name"`
				Status    string `json:"status"`
				StartTime int64  `json:"start_time"`
				EndTime   int64  `json:"end_time"`
			} `json:"info"`
			Data struct {
				Metrics []struct {
					Key   string            `json:"key"`
					Value mlflowMetricValue `json:"value"`
				} `json:"metrics"`
				Params []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"params"`
			} `json:"data"`
		} `json:"runs"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &payload); err != nil {
		return nil, nil, false, err
	}
	if len(payload.Runs) == 0 {
		return nil, nil, false, nil
	}
	raw := payload.Runs[0]
	if raw.Info.ID == "" {
		return nil, nil, false, fmt.Errorf("MLflow run response is incomplete")
	}
	run := &ExperimentRun{
		ID: raw.Info.ID, Name: raw.Info.Name, Status: raw.Info.Status,
		StartTimeMS: raw.Info.StartTime, EndTimeMS: raw.Info.EndTime,
		Latest: map[string]float64{}, Params: map[string]string{},
	}
	keys := make([]string, 0, len(raw.Data.Metrics))
	for _, metric := range raw.Data.Metrics {
		if !safeMetricKey(metric.Key) || len(keys) >= maxMLflowMetricKeys || !metric.Value.Valid {
			continue
		}
		run.Latest[metric.Key] = metric.Value.Value
		keys = append(keys, metric.Key)
	}
	for _, parameter := range raw.Data.Params {
		if len(run.Params) >= 100 || !safeMetricKey(parameter.Key) {
			break
		}
		run.Params[parameter.Key] = truncate(parameter.Value, 1024)
	}
	sort.Strings(keys)
	return run, keys, true, nil
}

func mlflowProvenanceTag(key []byte, jobID string) string {
	return domain.MLflowProvenanceTag(key, jobID)
}

func (c *MLflowClient) metricHistory(ctx context.Context, runID, key string) ([]MLflowMetricPoint, error) {
	endpoint, err := c.endpoint("/api/2.0/mlflow/metrics/get-history")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("run_id", runID)
	query.Set("metric_key", key)
	query.Set("max_results", fmt.Sprint(maxMLflowMetricPoints))
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Metrics []struct {
			Value     mlflowMetricValue `json:"value"`
			Timestamp int64             `json:"timestamp"`
			Step      int64             `json:"step"`
		} `json:"metrics"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &payload); err != nil {
		return nil, err
	}
	if len(payload.Metrics) > maxMLflowMetricPoints {
		payload.Metrics = payload.Metrics[len(payload.Metrics)-maxMLflowMetricPoints:]
	}
	points := make([]MLflowMetricPoint, 0, len(payload.Metrics))
	for _, metric := range payload.Metrics {
		if !metric.Value.Valid {
			continue
		}
		points = append(points, MLflowMetricPoint{Value: metric.Value.Value, TimestampMS: metric.Timestamp, Step: metric.Step})
	}
	return points, nil
}

func (c *MLflowClient) endpoint(apiPath string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + apiPath)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("parse MLflow URL: %w", err)
	}
	return endpoint, nil
}

func (c *MLflowClient) doJSON(ctx context.Context, method string, endpoint *url.URL, body any, target any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode MLflow request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return 0, fmt.Errorf("create MLflow request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("query MLflow: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return response.StatusCode, fmt.Errorf("MLflow returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxMLflowResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return response.StatusCode, fmt.Errorf("read MLflow response: %w", err)
	}
	if len(data) > maxMLflowResponseBytes {
		return response.StatusCode, fmt.Errorf("MLflow response exceeds size limit")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return response.StatusCode, fmt.Errorf("decode MLflow response: %w", err)
	}
	return response.StatusCode, nil
}

func safeMetricKey(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-/", char) {
			continue
		}
		return false
	}
	return true
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
