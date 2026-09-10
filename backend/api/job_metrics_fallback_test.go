package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type metricsFallbackProvider struct {
	metrics observability.JobMetrics
	err     error
}

func (provider metricsFallbackProvider) QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error) {
	return provider.metrics, provider.err
}

type metricsFallbackExperimentProvider struct {
	experiment observability.JobExperiment
	calls      int
}

func (provider *metricsFallbackExperimentProvider) QueryJobExperiment(context.Context, string, string) (observability.JobExperiment, error) {
	provider.calls++
	return provider.experiment, nil
}

func (provider *metricsFallbackExperimentProvider) ListTenantExperiments(context.Context, string, string, int) (observability.ExperimentCatalog, error) {
	return observability.ExperimentCatalog{}, nil
}

func TestGetJobMetricsFallsBackToVerifiedMLflowSeries(t *testing.T) {
	started := time.Unix(1_700_000_000, 0).UTC()
	provider := &metricsFallbackExperimentProvider{experiment: observability.JobExperiment{Series: []observability.MLflowMetricSeries{
		{Key: "loss", Points: []observability.MLflowMetricPoint{{Value: 2.5, TimestampMS: started.UnixMilli(), Step: 10}, {Value: 1.25, TimestampMS: started.Add(time.Minute).UnixMilli(), Step: 20}}},
		{Key: "lr", Points: []observability.MLflowMetricPoint{{Value: 0.001, TimestampMS: started.UnixMilli(), Step: 10}}},
		{Key: "epoch", Points: []observability.MLflowMetricPoint{{Value: 2, TimestampMS: started.Add(time.Minute).UnixMilli(), Step: 20}}},
	}}}
	handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-a"}}}, Options{
		Metrics:     metricsFallbackProvider{},
		Experiments: provider,
	})
	principal := auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	trainingPerformanceRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/metrics", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected MLflow fallback response, got status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data observability.JobMetrics `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode metrics response: %v", err)
	}
	if provider.calls != 1 || envelope.Data.Loss == nil || *envelope.Data.Loss != 1.25 || envelope.Data.LearningRate == nil || *envelope.Data.LearningRate != 0.001 || envelope.Data.Epoch == nil || *envelope.Data.Epoch != 2 {
		t.Fatalf("MLflow fallback did not provide canonical curves: calls=%d metrics=%+v", provider.calls, envelope.Data)
	}
	for _, series := range envelope.Data.Series {
		if series.Name == "loss" && len(series.Points) == 2 && series.Points[1].Timestamp.Equal(started.Add(time.Minute)) {
			return
		}
	}
	t.Fatalf("loss history was not returned: %+v", envelope.Data.Series)
}

func TestMLflowFallbackNeverReplacesPrometheusCurve(t *testing.T) {
	prometheusLoss := 9.0
	started := time.Unix(1_700_000_000, 0).UTC()
	merged := mergeMLflowMetricFallback(observability.JobMetrics{Loss: &prometheusLoss}, observability.JobExperiment{Series: []observability.MLflowMetricSeries{
		{Key: "loss", Points: []observability.MLflowMetricPoint{{Value: 1, TimestampMS: started.UnixMilli()}}},
		{Key: "lr", Points: []observability.MLflowMetricPoint{{Value: 0.01, TimestampMS: started.UnixMilli()}}},
		{Key: "untrusted_metric", Points: []observability.MLflowMetricPoint{{Value: 100, TimestampMS: started.UnixMilli()}}},
	}})

	if merged.Loss == nil || *merged.Loss != prometheusLoss || merged.LearningRate == nil || *merged.LearningRate != 0.01 {
		t.Fatalf("unexpected merged metric values: %+v", merged)
	}
	if len(merged.Series) != 1 || merged.Series[0].Name != "learningRate" {
		t.Fatalf("fallback must retain only missing canonical curves: %+v", merged.Series)
	}
}

func TestCanonicalMLflowCurveNameSupportsFrameworkPrefixes(t *testing.T) {
	tests := map[string]string{
		"train/loss":               "loss",
		"train/learning_rate":      "learningRate",
		"train/lr":                 "learningRate",
		"train/throughput":         "throughput",
		"train/samples_per_second": "throughput",
		"train/epoch":              "epoch",
	}
	for key, want := range tests {
		got, ok := canonicalMLflowCurveName(key)
		if !ok || got != want {
			t.Errorf("canonicalMLflowCurveName(%q) = %q, %v; want %q, true", key, got, ok, want)
		}
	}
	if got, ok := canonicalMLflowCurveName("val/loss"); ok {
		t.Fatalf("validation loss must not replace the training loss curve: %q", got)
	}
}

func TestGetJobMetricsDoesNotQueryMLflowWhenPrometheusIsComplete(t *testing.T) {
	loss, throughput, learningRate, epoch := 1.0, 2.0, 0.01, 3.0
	provider := &metricsFallbackExperimentProvider{}
	handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-a"}}}, Options{
		Metrics: metricsFallbackProvider{metrics: observability.JobMetrics{
			Loss: &loss, Throughput: &throughput, LearningRate: &learningRate, Epoch: &epoch,
		}},
		Experiments: provider,
	})
	principal := auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	trainingPerformanceRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/metrics", nil))

	if response.Code != http.StatusOK || provider.calls != 0 {
		t.Fatalf("complete Prometheus response should not query MLflow: status=%d calls=%d body=%s", response.Code, provider.calls, response.Body.String())
	}
}
