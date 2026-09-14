package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMLflowMetricSelectionPreservesKeyMetricsBeyondLimit(t *testing.T) {
	metrics := []map[string]any{{"key": "loss", "value": "NaN"}}
	for i := 0; i < 350; i++ {
		metrics = append(metrics, map[string]any{"key": fmt.Sprintf("val/object/class_%03d", i), "value": float64(i)})
	}
	important := map[string]float64{"train/loss": 0.12, "train/lr": 0.001, "epoch": 12, "val/object/map": 0.57, "val/object/nds": 0.60}
	for key, value := range important {
		metrics = append(metrics, map[string]any{"key": key, "value": value})
	}
	historyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/2.0/mlflow/experiments/get-by-name":
			_, _ = w.Write([]byte(`{"experiment":{"experiment_id":"1"}}`))
		case "/api/2.0/mlflow/runs/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []any{map[string]any{
				"info": map[string]any{"run_id": "run-1"},
				"data": map[string]any{"metrics": metrics, "tags": []any{
					map[string]string{"key": "platform.job_id", "value": "job-01"},
					map[string]string{"key": "platform.provenance", "value": mlflowProvenanceTag(testProvenanceKey, "job-01")},
				}},
			}}})
		case "/api/2.0/mlflow/metrics/get-history":
			historyCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"metrics": []any{map[string]any{"key": r.URL.Query().Get("metric_key"), "value": 1, "timestamp": 1000, "step": 1}}})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := &MLflowClient{BaseURL: server.URL, ProvenanceKey: testProvenanceKey}
	catalog, err := client.ListTenantExperiments(context.Background(), "local", "", 10)
	if err != nil || len(catalog.Runs) != 1 {
		t.Fatalf("catalog: %#v, %v", catalog, err)
	}
	job, err := client.QueryJobExperiment(context.Background(), "local", "job-01")
	if err != nil || job.Run == nil {
		t.Fatalf("job: %#v, %v", job, err)
	}
	for label, latest := range map[string]map[string]float64{"catalog": catalog.Runs[0].Latest, "job": job.Run.Latest} {
		for key, value := range important {
			if got, ok := latest[key]; !ok || got != value {
				t.Errorf("%s lost key metric %s: %v, present=%v", label, key, got, ok)
			}
		}
		if len(latest) != maxMLflowMetricKeys {
			t.Errorf("%s metric bound: %d", label, len(latest))
		}
		if _, ok := latest["loss"]; ok {
			t.Errorf("%s returned non-finite loss", label)
		}
	}
	if historyCalls != maxMLflowMetricKeys {
		t.Errorf("history requests = %d, want %d", historyCalls, maxMLflowMetricKeys)
	}
	for left, right := 0, len(metrics)-1; left < right; left, right = left+1, right-1 {
		metrics[left], metrics[right] = metrics[right], metrics[left]
	}
	reordered, err := client.ListTenantExperiments(context.Background(), "local", "", 10)
	if err != nil || len(reordered.Runs) != 1 || !reflect.DeepEqual(catalog.Runs[0].Latest, reordered.Runs[0].Latest) {
		t.Errorf("metric selection depends on upstream response order: %v", err)
	}
}
