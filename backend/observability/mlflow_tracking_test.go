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

	"ray-train-platform-backend/mlflowtracking"
)

func TestSanitizeTrackingReadbackKeepsLatestMetadataAndOnlyUserTags(t *testing.T) {
	var raw mlflowIntegrationRun
	if err := json.Unmarshal([]byte(`{"data":{"metrics":[{"key":"loss","value":0.25,"timestamp":2000,"step":7}],"tags":[{"key":"review","value":"candidate"},{"key":"platform.operation_id","value":"hidden"},{"key":"mlflow.runName","value":"hidden"},{"key":"provenance","value":"hidden"},{"key":"credential","value":"hidden"},{"key":"internal.trace","value":"hidden"},{"key":"systemtag","value":"hidden"},{"key":"system.version","value":"hidden"},{"key":"access_token","value":"hidden"},{"key":"invalid key","value":"hidden"}]}}`), &raw); err != nil { t.Fatal(err) }
	snapshot := sanitizeTrackingRun(raw)
	encoded, err := json.Marshal(snapshot)
	if err != nil { t.Fatal(err) }
	var readback struct {
		LatestMetrics map[string]mlflowtracking.MetricPoint
		Tags map[string]string
	}
	if err := json.Unmarshal(encoded, &readback); err != nil { t.Fatal(err) }
	if readback.LatestMetrics["loss"] != (mlflowtracking.MetricPoint{Value: 0.25, TimestampMS: 2000, Step: 7}) {
		t.Fatalf("latest metadata missing: %s", encoded)
	}
	if len(readback.Tags) != 1 || readback.Tags["review"] != "candidate" { t.Fatalf("unsafe or missing tags: %s", encoded) }
}

func TestSanitizeTrackingTagsAreBoundedAndDeterministic(t *testing.T) {
	var raw mlflowIntegrationRun
	for i := 104; i >= 0; i-- { raw.Data.Tags = append(raw.Data.Tags, MLflowKeyValue{Key: fmt.Sprintf("user.%03d", i), Value: "accepted"}) }
	raw.Data.Tags = append(raw.Data.Tags, MLflowKeyValue{Key: "a.too_long", Value: strings.Repeat("x", 5001)}, MLflowKeyValue{Key: "a.invalid_utf8", Value: string([]byte{0xff})})
	encoded, err := json.Marshal(sanitizeTrackingRun(raw))
	if err != nil { t.Fatal(err) }
	var readback struct { Tags map[string]string }
	if err := json.Unmarshal(encoded, &readback); err != nil { t.Fatal(err) }
	if len(readback.Tags) != 100 || readback.Tags["user.000"] != "accepted" || readback.Tags["user.099"] != "accepted" || readback.Tags["user.100"] != "" {
		t.Fatalf("readback must select first 100 sorted valid keys: %s", encoded)
	}
}

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

func TestMLflowTrackingCreateExperimentReusesFoundExperimentWithoutCreate(t *testing.T) {
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/experiments/get-by-name":
			_ = json.NewEncoder(w).Encode(map[string]any{"experiment": map[string]any{"experiment_id": "42"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/experiments/create":
			creates++
			t.Fatalf("found experiment should not be created again")
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
	if upstreamID != "42" || creates != 0 {
		t.Fatalf("unexpected reuse id=%q creates=%d", upstreamID, creates)
	}
}

func TestMLflowTrackingCreateExperimentReconcilesAfterAmbiguousCreate(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/experiments/get-by-name":
			gets++
			if gets == 1 {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"experiment": map[string]any{"experiment_id": "43"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/experiments/create":
			http.Error(w, "ambiguous upstream failure", http.StatusBadGateway)
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
	if upstreamID != "43" || gets != 2 {
		t.Fatalf("unexpected reconciliation id=%q gets=%d", upstreamID, gets)
	}
}

func TestMLflowTrackingCreateExperimentRejectsIncompleteCreateResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/experiments/get-by-name":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/mlflow/experiments/create":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	if _, err := client.CreateExperiment(context.Background(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, mlflowtracking.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for incomplete response, got %v", err)
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

func TestMLflowTrackingCreateRunFallsBackWhenCreateResponseOmitsProof(t *testing.T) {
	searches := 0
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
			_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"info": map[string]any{"run_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "experiment_id": "9"}}})
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
	if upstreamID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || searches != 2 {
		t.Fatalf("unexpected fallback id=%q searches=%d", upstreamID, searches)
	}
}

func TestMLflowTrackingFindRunHandlesMissingWrongProofAndDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runs    []any
		found   bool
		wantErr error
	}{
		{name: "missing", runs: []any{}, found: false},
		{name: "wrong proof", runs: []any{trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "RUNNING", "00000000000000000000000000000000", nil, nil)}, found: false},
		{name: "duplicate", runs: []any{
			trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "RUNNING", "fedcba9876543210fedcba9876543210", nil, nil),
			trackingRunPayload("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "9", "external", "RUNNING", "fedcba9876543210fedcba9876543210", nil, nil),
		}, wantErr: mlflowtracking.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/mlflow/runs/search" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"runs": tc.runs})
			}))
			defer server.Close()

			client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
			_, found, err := client.FindRun(context.Background(), "9", "fedcba9876543210fedcba9876543210")
			if !errors.Is(err, tc.wantErr) || found != tc.found {
				t.Fatalf("unexpected find result found=%t err=%v", found, err)
			}
		})
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

func TestMLflowTrackingReadRunMetricHistoryFailureIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/runs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"run": trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "RUNNING", "fedcba9876543210fedcba9876543210", []map[string]any{{"key": "loss", "value": 1.5}}, nil)})
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/mlflow/metrics/get-history":
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
	if _, err := client.ReadRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210"); !errors.Is(err, mlflowtracking.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for metric history failure, got %v", err)
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

func TestMLflowTrackingRejectsInvalidAdapterConfigBeforeHTTP(t *testing.T) {
	client := &MLflowClient{BaseURL: "http://127.0.0.1", ExperimentPrefix: "bad/prefix", ProvenanceKey: []byte(strings.Repeat("k", 32))}
	if _, err := client.CreateExperiment(context.Background(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, mlflowtracking.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad prefix, got %v", err)
	}
	client.ExperimentPrefix = "raytrain"
	client.ProvenanceKey = nil
	if _, err := client.CreateExperiment(context.Background(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, mlflowtracking.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for missing provenance key, got %v", err)
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
				RunID   string `json:"run_id"`
				Status  string `json:"status"`
				EndTime int64  `json:"end_time"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			finishedStatus = body.Status
			if body.EndTime != 12345 {
				t.Fatalf("unexpected end_time %d", body.EndTime)
			}
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
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED", 12345); err != nil {
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

func TestMLflowTrackingLogRunRejectsTerminalRunAndInvalidBatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		batch   mlflowtracking.Batch
		wantErr error
	}{
		{name: "terminal", status: "FINISHED", batch: mlflowtracking.Batch{Tags: []mlflowtracking.Pair{{Key: "ok", Value: "yes"}}}, wantErr: mlflowtracking.ErrConflict},
		{name: "invalid batch", status: "RUNNING", batch: mlflowtracking.Batch{}, wantErr: mlflowtracking.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/2.0/mlflow/runs/get" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"run": trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", tc.status, "fedcba9876543210fedcba9876543210", nil, nil)})
			}))
			defer server.Close()

			client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32)), HTTPClient: server.Client()}
			err := client.LogRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", tc.batch)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
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
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED", 12345); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("unexpected update count %d", updates)
	}
}

func TestMLflowTrackingFinishRunRejectsInvalidStatusAndEndTime(t *testing.T) {
	client := &MLflowClient{BaseURL: "http://127.0.0.1", ExperimentPrefix: "raytrain", ProvenanceKey: []byte(strings.Repeat("k", 32))}
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "RUNNING", 12345); !errors.Is(err, mlflowtracking.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for non-terminal status, got %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/2.0/mlflow/runs/get" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run": trackingRunPayload("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9", "external", "RUNNING", "fedcba9876543210fedcba9876543210", nil, nil)})
	}))
	defer server.Close()
	client.HTTPClient = server.Client()
	client.BaseURL = server.URL
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED", -1); !errors.Is(err, mlflowtracking.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for negative end time, got %v", err)
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
	if err := client.FinishRun(context.Background(), "9", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fedcba9876543210fedcba9876543210", "FINISHED", 12345); err != nil {
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
