package observability

import (
	"context"
	"crypto/hmac"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var mlflowIntegrationRunID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type mlflowIntegrationRun struct {
	Info struct {
		ID           string `json:"run_id"`
		ExperimentID string `json:"experiment_id"`
		Name         string `json:"run_name"`
		Status       string `json:"status"`
		StartTime    int64  `json:"start_time"`
		EndTime      int64  `json:"end_time"`
	} `json:"info"`
	Data struct {
		Metrics []struct {
			Key       string            `json:"key"`
			Value     mlflowMetricValue `json:"value"`
			Timestamp *int64            `json:"timestamp"`
			Step      *int64            `json:"step"`
		} `json:"metrics"`
		Params []MLflowKeyValue `json:"params"`
		Tags   []MLflowKeyValue `json:"tags"`
	} `json:"data"`
}

// QueryJobRun reads the requested attempt, never the latest run for a job.
// The caller supplies the owner from the platform job record, not the request.
func (c *MLflowClient) QueryJobRun(ctx context.Context, tenant, job, owner, runID string) (JobExperiment, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, name, err := c.integrationRun(ctx, tenant, job, owner, runID, false)
	if err != nil {
		return JobExperiment{}, err
	}
	run, keys := sanitizeIntegrationRun(raw)
	result := JobExperiment{ExperimentName: name, Run: run, Series: []MLflowMetricSeries{}}
	for _, key := range keys {
		points, err := c.metricHistory(ctx, runID, key)
		if err != nil {
			return JobExperiment{}, err
		}
		result.Series = append(result.Series, MLflowMetricSeries{Key: key, Points: points})
	}
	return result, nil
}

// LogJobRunBatch does not create or change run lifecycle state. Native MLflow
// batch writes are not transactional; a failed response can follow partial writes.
func (c *MLflowClient) LogJobRunBatch(ctx context.Context, tenant, job, owner, runID string, batch MLflowLogBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	run, _, err := c.integrationRun(ctx, tenant, job, owner, runID, true)
	if err != nil {
		return err
	}
	if run.Info.Status != "RUNNING" {
		return ErrMLflowRunNotRunning
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/log-batch")
	if err != nil {
		return err
	}
	body := struct {
		RunID string `json:"run_id"`
		MLflowLogBatch
	}{RunID: runID, MLflowLogBatch: batch}
	var result map[string]any
	status, err := c.doJSON(ctx, http.MethodPost, endpoint, body, &result)
	if status == http.StatusBadRequest || status == http.StatusConflict {
		return ErrMLflowBatchConflict
	}
	return err
}

func (c *MLflowClient) integrationRun(ctx context.Context, tenant, job, owner, runID string, write bool) (mlflowIntegrationRun, string, error) {
	var zero mlflowIntegrationRun
	if c == nil || strings.TrimSpace(c.BaseURL) == "" || len(c.ProvenanceKey) < 32 {
		return zero, "", fmt.Errorf("MLflow integration is not configured")
	}
	if !safeLabelValue(tenant) || !safeLabelValue(job) || strings.TrimSpace(owner) == "" || len(owner) > 256 || !mlflowIntegrationRunID.MatchString(runID) {
		return zero, "", ErrMLflowRunNotFound
	}
	prefix := strings.Trim(strings.TrimSpace(c.ExperimentPrefix), "-")
	if prefix == "" {
		prefix = "raytrain"
	}
	if !safeLabelValue(prefix) {
		return zero, "", fmt.Errorf("MLflow experiment prefix is invalid")
	}
	name := prefix + "-" + tenant
	experimentID, found, err := c.experimentID(ctx, name)
	if err != nil {
		return zero, "", err
	}
	if !found {
		return zero, "", ErrMLflowRunNotFound
	}
	endpoint, err := c.endpoint("/api/2.0/mlflow/runs/get")
	if err != nil {
		return zero, "", err
	}
	query := endpoint.Query()
	query.Set("run_id", runID)
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Run mlflowIntegrationRun `json:"run"`
	}
	status, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &payload)
	if status == http.StatusNotFound {
		return zero, "", ErrMLflowRunNotFound
	}
	if err != nil {
		return zero, "", err
	}
	run := payload.Run
	if run.Info.ID != runID || run.Info.ExperimentID != experimentID || !c.integrationTagsMatch(run, tenant, job, owner, write) {
		return zero, "", ErrMLflowRunNotFound
	}
	return run, name, nil
}

func (c *MLflowClient) integrationTagsMatch(run mlflowIntegrationRun, tenant, job, owner string, write bool) bool {
	tags := make(map[string]string, len(run.Data.Tags))
	for _, tag := range run.Data.Tags {
		if _, exists := tags[tag.Key]; exists {
			return false
		}
		tags[tag.Key] = tag.Value
	}
	if tags["platform.job_id"] != job || !hmac.Equal([]byte(tags["platform.provenance"]), []byte(mlflowProvenanceTag(c.ProvenanceKey, job))) {
		return false
	}
	for key, want := range map[string]string{"platform.tenant_id": tenant, "platform.submitter_user_id": owner} {
		got, present := tags[key]
		if (present || write) && got != want {
			return false
		}
	}
	return true
}

func sanitizeIntegrationRun(raw mlflowIntegrationRun) (*ExperimentRun, []string) {
	run := &ExperimentRun{ID: raw.Info.ID, Name: raw.Info.Name, Status: raw.Info.Status, StartTimeMS: raw.Info.StartTime, EndTimeMS: raw.Info.EndTime, Latest: map[string]float64{}, Params: map[string]string{}}
	for _, metric := range raw.Data.Metrics {
		if len(run.Latest) < maxMLflowMetricKeys && safeMetricKey(metric.Key) && metric.Value.Valid {
			run.Latest[metric.Key] = metric.Value.Value
		}
	}
	for _, param := range raw.Data.Params {
		if len(run.Params) < 100 && validMLflowIntegrationKey(param.Key) {
			run.Params[param.Key] = truncate(param.Value, 1024)
		}
	}
	keys := make([]string, 0, len(run.Latest))
	for key := range run.Latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return run, keys
}
