package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const integrationRunID = "0123456789abcdef0123456789abcdef"

func integrationRunPayload(status string) map[string]any {
	return map[string]any{
		"info": map[string]any{"run_id": integrationRunID, "experiment_id": "7", "run_name": "older-attempt", "status": status, "artifact_uri": "s3://private"},
		"data": map[string]any{
			"params":  []map[string]any{{"key": "epochs", "value": "5"}},
			"metrics": []map[string]any{{"key": "loss", "value": 0.5}},
			"tags":    []map[string]any{{"key": "platform.job_id", "value": "job-01"}, {"key": "platform.tenant_id", "value": "team-a"}, {"key": "platform.submitter_user_id", "value": "user-a"}, {"key": "platform.provenance", "value": mlflowProvenanceTag([]byte(strings.Repeat("k", 32)), "job-01")}},
		},
	}
}

func integrationClient(t *testing.T, run map[string]any, writes *int) *MLflowClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/2.0/mlflow/experiments/get-by-name":
			if r.URL.Query().Get("experiment_name") != "raytrain-team-a" {
				t.Error("wrong tenant experiment")
			}
			fmt.Fprint(w, `{"experiment":{"experiment_id":"7"}}`)
		case "/api/2.0/mlflow/runs/get":
			if r.URL.Query().Get("run_id") != integrationRunID {
				t.Error("wrong exact run")
			}
			json.NewEncoder(w).Encode(map[string]any{"run": run})
		case "/api/2.0/mlflow/metrics/get-history":
			if r.URL.Query().Get("run_id") != integrationRunID {
				t.Error("history from wrong run")
			}
			fmt.Fprint(w, `{"metrics":[{"value":0.5,"timestamp":1000,"step":1}]}`)
		case "/api/2.0/mlflow/runs/log-batch":
			*writes++
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			for _, field := range []string{"metrics", "params", "tags"} {
				if value, present := body[field]; present && value == nil {
					t.Errorf("upstream %s must be omitted or an array, not null", field)
				}
			}
			if body["run_id"] != integrationRunID || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("unsafe upstream request")
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	return &MLflowClient{BaseURL: server.URL, ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
}

func TestMLflowIntegrationReadsExactOlderRunAndSanitizes(t *testing.T) {
	writes := 0
	client := integrationClient(t, integrationRunPayload("FINISHED"), &writes)
	result, err := client.QueryJobRun(context.Background(), "team-a", "job-01", "user-a", integrationRunID)
	if err != nil || result.Run == nil || result.Run.ID != integrationRunID || result.Run.Params["epochs"] != "5" || len(result.Series) != 1 {
		t.Fatalf("exact run: %+v %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "s3://") || strings.Contains(string(encoded), "provenance") {
		t.Fatalf("leaked internal fields: %s", encoded)
	}
}

func TestMLflowIntegrationRejectsForgedRunBindings(t *testing.T) {
	for _, field := range []string{"run_id", "experiment_id", "platform.job_id", "platform.tenant_id", "platform.submitter_user_id", "platform.provenance"} {
		t.Run(field, func(t *testing.T) {
			run := integrationRunPayload("RUNNING")
			if strings.HasPrefix(field, "platform.") {
				for _, tag := range run["data"].(map[string]any)["tags"].([]map[string]any) {
					if tag["key"] == field {
						tag["value"] = "forged"
					}
				}
			} else {
				run["info"].(map[string]any)[field] = "forged"
			}
			writes := 0
			client := integrationClient(t, run, &writes)
			if _, err := client.QueryJobRun(context.Background(), "team-a", "job-01", "user-a", integrationRunID); !errors.Is(err, ErrMLflowRunNotFound) {
				t.Fatalf("forged binding accepted: %v", err)
			}
			if err := client.LogJobRunBatch(context.Background(), "team-a", "job-01", "user-a", integrationRunID, MLflowLogBatch{Params: []MLflowKeyValue{{Key: "epochs", Value: "5"}}}); !errors.Is(err, ErrMLflowRunNotFound) || writes != 0 {
				t.Fatalf("forged write: %v writes=%d", err, writes)
			}
		})
	}
}

func TestMLflowIntegrationWritesOnlyRunningRun(t *testing.T) {
	for _, status := range []string{"RUNNING", "FINISHED", "FAILED", "KILLED"} {
		t.Run(status, func(t *testing.T) {
			writes := 0
			client := integrationClient(t, integrationRunPayload(status), &writes)
			err := client.LogJobRunBatch(context.Background(), "team-a", "job-01", "user-a", integrationRunID, MLflowLogBatch{Params: []MLflowKeyValue{{Key: "epochs", Value: "5"}}})
			if status == "RUNNING" {
				if err != nil || writes != 1 {
					t.Fatalf("write failed: %v %d", err, writes)
				}
			} else if !errors.Is(err, ErrMLflowRunNotRunning) || writes != 0 {
				t.Fatalf("terminal write allowed: %v %d", err, writes)
			}
		})
	}
}

func TestMLflowIntegrationLegacyRunReadableButNotWritable(t *testing.T) {
	run := integrationRunPayload("RUNNING")
	tags := run["data"].(map[string]any)["tags"].([]map[string]any)
	run["data"].(map[string]any)["tags"] = []map[string]any{tags[0], tags[3]}
	writes := 0
	client := integrationClient(t, run, &writes)
	if _, err := client.QueryJobRun(context.Background(), "team-a", "job-01", "user-a", integrationRunID); err != nil {
		t.Fatalf("legacy read rejected: %v", err)
	}
	if err := client.LogJobRunBatch(context.Background(), "team-a", "job-01", "user-a", integrationRunID, MLflowLogBatch{Params: []MLflowKeyValue{{Key: "epochs", Value: "5"}}}); !errors.Is(err, ErrMLflowRunNotFound) || writes != 0 {
		t.Fatalf("legacy write accepted: %v", err)
	}
}

func TestMLflowIntegrationSingleFieldBatchesDoNotSendNullArrays(t *testing.T) {
	value, timestamp, step := 0.5, int64(1000), int64(1)
	for _, batch := range []MLflowLogBatch{
		{Metrics: []MLflowLogMetric{{Key: "loss", Value: &value, Timestamp: &timestamp, Step: &step}}},
		{Params: []MLflowKeyValue{{Key: "epochs", Value: "5"}}},
		{Tags: []MLflowKeyValue{{Key: "review", Value: "candidate"}}},
	} {
		writes := 0
		client := integrationClient(t, integrationRunPayload("RUNNING"), &writes)
		if err := client.LogJobRunBatch(context.Background(), "team-a", "job-01", "user-a", integrationRunID, batch); err != nil || writes != 1 {
			t.Fatalf("single-field batch failed: %v writes=%d", err, writes)
		}
	}
}
