package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ray-train-platform-backend/mlflowtracking"
)

func TestMLflowTrackingCreateExperimentFindsDeterministicNameBeforeCreate(t *testing.T) {
	var createdName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/experiments/get-by-name":
			if got := r.URL.Query().Get("experiment_name"); got != "raytrain-external-0123456789abcdef0123456789abcdef" {
				t.Fatalf("unexpected experiment name %q", got)
			}
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/experiments/create":
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			createdName = body.Name
			_ = json.NewEncoder(w).Encode(map[string]string{"experiment_id": "17"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	upstreamID, err := client.CreateExperiment(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamID != "17" || createdName != "raytrain-external-0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected result id=%q name=%q", upstreamID, createdName)
	}
}

func TestMLflowTrackingCreateRunReconcilesByOperationTagBeforeRetryingCreate(t *testing.T) {
	searches := 0
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/runs/search":
			searches++
			if searches == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"runs": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []any{trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "RUNNING", "fedcba9876543210fedcba9876543210", nil, nil)}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/runs/create":
			creates++
			http.Error(w, "ambiguous upstream failure", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	upstreamID, err := client.CreateRun(context.Background(), "9", "fedcba9876543210fedcba9876543210", "external")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || searches != 2 || creates != 1 {
		t.Fatalf("unexpected reconciliation id=%q searches=%d creates=%d", upstreamID, searches, creates)
	}
}

func TestMLflowTrackingReadRunRequiresExperimentAndOperationProof(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/2.0/mlflow/runs/get" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": trackingRunPayload(
				"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"different-experiment",
				"external",
				"RUNNING",
				"fedcba9876543210fedcba9876543210",
				nil,
				nil,
			),
		})
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	if _, err := client.ReadRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210"); err == nil {
		t.Fatal("expected proof mismatch to fail")
	}
}

func TestMLflowTrackingRejectsInvalidUpstreamIdentifiersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("invalid upstream identifiers should not reach HTTP: %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	operationID := "fedcba9876543210fedcba9876543210"
	if _, _, err := client.FindRun(context.Background(), "not-numeric", operationID); !errors.Is(err, mlflowtracking.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for invalid experiment id, got %v", err)
	}
	if _, err := client.ReadRun(context.Background(), "9", "not-a-run-id", operationID); !errors.Is(err, mlflowtracking.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for invalid run id, got %v", err)
	}
	if called {
		t.Fatal("invalid identifiers reached upstream HTTP")
	}
}

func TestMLflowTrackingLogFinishAndHistoryUseVerifiedRun(t *testing.T) {
	var loggedBatch mlflowtracking.Batch
	var finishedStatus string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/runs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"run": trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "RUNNING", "fedcba9876543210fedcba9876543210", []map[string]any{{"key": "loss", "value": 1.5}}, []map[string]string{{"key": "lr", "value": "0.1"}})})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/runs/log-batch":
			var body struct {
				RunID string `json:"run_id"`
				mlflowtracking.Batch
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.RunID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
				t.Fatalf("unexpected log run id %q", body.RunID)
			}
			loggedBatch = body.Batch
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/runs/update":
			var body struct {
				RunID  string `json:"run_id"`
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			finishedStatus = body.Status
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/metrics/get-history":
			_ = json.NewEncoder(w).Encode(map[string]any{"metrics": []map[string]any{{"value": 1.5, "timestamp": 11, "step": 1}}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	value := 2.5
	ts := int64(22)
	step := int64(2)
	if err := client.LogRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", mlflowtracking.Batch{
		Metrics: []mlflowtracking.Metric{{Key: "acc", Value: &value, Timestamp: &ts, Step: &step}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(loggedBatch.Metrics) != 1 || loggedBatch.Metrics[0].Key != "acc" {
		t.Fatalf("unexpected logged batch %#v", loggedBatch)
	}
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED"); err != nil {
		t.Fatal(err)
	}
	if finishedStatus != "FINISHED" {
		t.Fatalf("unexpected finished status %q", finishedStatus)
	}
	snapshot, err := client.ReadRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "RUNNING" || snapshot.Latest["loss"] != 1.5 || snapshot.Params["lr"] != "0.1" || len(snapshot.Series) != 1 {
		t.Fatalf("unexpected snapshot %#v", snapshot)
	}
}

func TestMLflowTrackingFinishRunAlreadyTerminalIsIdempotent(t *testing.T) {
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/runs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"run": trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "FINISHED", "fedcba9876543210fedcba9876543210", nil, nil)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/runs/update":
			updates++
			t.Fatalf("already terminal run should not be updated")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED"); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("unexpected update count %d", updates)
	}
}

func TestMLflowTrackingFinishRunReconcilesAmbiguousUpdate(t *testing.T) {
	gets := 0
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/runs/get":
			gets++
			status := "RUNNING"
			if gets > 1 {
				status = "FINISHED"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"run": trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", status, "fedcba9876543210fedcba9876543210", nil, nil)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/runs/update":
			updates++
			http.Error(w, "ambiguous upstream failure", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED"); err != nil {
		t.Fatal(err)
	}
	if gets != 2 || updates != 1 {
		t.Fatalf("unexpected reconciliation calls gets=%d updates=%d", gets, updates)
	}
}

func trackingRunPayload(runID, experimentID, name, status, operationID string, metrics []map[string]any, params []map[string]string) map[string]any {
	tags := []map[string]string{
		{"key": "platform.operation_id", "value": operationID},
		{"key": "platform.provenance", "value": externalOperationProvenance([]byte(strings.Repeat("k", 32)), operationID)},
	}
	return map[string]any{
		"info": map[string]any{
			"run_id":        runID,
			"experiment_id": experimentID,
			"run_name":      name,
			"status":        status,
			"start_time":    int64(10),
			"end_time":      int64(20),
		},
		"data": map[string]any{
			"metrics": metrics,
			"params":  params,
			"tags":    tags,
		},
	}
}
