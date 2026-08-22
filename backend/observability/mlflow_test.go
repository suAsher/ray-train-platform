package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testProvenanceKey = []byte("0123456789abcdef0123456789abcdef")

func TestMLflowClientReturnsOnlyJobScopedMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/2.0/mlflow/experiments/get-by-name":
			if got := request.URL.Query().Get("experiment_name"); got != "raytrain-tenant-a" {
				t.Fatalf("unexpected experiment name %q", got)
			}
			_, _ = response.Write([]byte(`{"experiment":{"experiment_id":"7","name":"raytrain-tenant-a","artifact_location":"/secret/artifacts"}}`))
		case "/api/2.0/mlflow/runs/search":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			wantFilter := "tags.`platform.job_id` = 'job-01' AND tags.`platform.provenance` = '" + mlflowProvenanceTag(testProvenanceKey, "job-01") + "'"
			if filter, _ := body["filter"].(string); filter != wantFilter {
				t.Fatalf("search must be job scoped, got %q", filter)
			}
			_, _ = response.Write([]byte(`{"runs":[{"info":{"run_id":"run-1","run_name":"job-01","status":"FINISHED","start_time":1000,"end_time":2000,"artifact_uri":"/secret/artifacts/run-1"},"data":{"metrics":[{"key":"loss","value":1.5,"timestamp":2000,"step":2},{"key":"learning_rate","value":0.001,"timestamp":2000,"step":2},{"key":"invalid_metric","value":"NaN","timestamp":2000,"step":2}],"params":[{"key":"batch_size","value":"16"}],"tags":[{"key":"platform.job_id","value":"job-01"},{"key":"platform.user_id","value":"private-user"}]}}]}`))
		case "/api/2.0/mlflow/metrics/get-history":
			key := request.URL.Query().Get("metric_key")
			if request.URL.Query().Get("run_id") != "run-1" {
				t.Fatal("metric history must be scoped to the selected run")
			}
			if key == "loss" {
				_, _ = response.Write([]byte(`{"metrics":[{"key":"loss","value":3.0,"timestamp":1000,"step":1},{"key":"loss","value":1.5,"timestamp":2000,"step":2}]}`))
				return
			}
			_, _ = response.Write([]byte(`{"metrics":[{"key":"learning_rate","value":0.001,"timestamp":2000,"step":2}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: testProvenanceKey, HTTPClient: server.Client()}
	result, err := client.QueryJobExperiment(context.Background(), "tenant-a", "job-01")
	if err != nil {
		t.Fatalf("query MLflow: %v", err)
	}
	if result.ExperimentName != "raytrain-tenant-a" || result.Run == nil || result.Run.ID != "run-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(result.Series) != 2 || result.Series[0].Key != "learning_rate" || result.Series[1].Key != "loss" {
		t.Fatalf("metric series must be stable and sorted: %#v", result.Series)
	}
	if _, exists := result.Run.Latest["invalid_metric"]; exists {
		t.Fatalf("non-finite MLflow metrics must be ignored: %#v", result.Run.Latest)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "secret/artifacts") || strings.Contains(string(encoded), "private-user") {
		t.Fatalf("platform response leaked internal MLflow fields: %s", encoded)
	}
}

func TestMLflowClientRejectsUnsafeTenantAndJobIdentifiers(t *testing.T) {
	client := &MLflowClient{BaseURL: "http://mlflow.invalid", ExperimentPrefix: "raytrain", ProvenanceKey: testProvenanceKey}
	for _, test := range []struct{ tenant, job string }{
		{tenant: "tenant/a", job: "job-01"},
		{tenant: "tenant-a", job: "job' OR 1=1"},
	} {
		if _, err := client.QueryJobExperiment(context.Background(), test.tenant, test.job); err == nil {
			t.Fatalf("expected unsafe identifier rejection for %#v", test)
		}
	}
}

func TestMLflowClientReturnsEmptyExperimentWhenJobHasNoRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/2.0/mlflow/experiments/get-by-name":
			_, _ = response.Write([]byte(`{"experiment":{"experiment_id":"7","name":"raytrain-tenant-a"}}`))
		case "/api/2.0/mlflow/runs/search":
			_, _ = response.Write([]byte(`{"runs":[]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: testProvenanceKey, HTTPClient: server.Client()}
	result, err := client.QueryJobExperiment(context.Background(), "tenant-a", "job-01")
	if err != nil || result.Run != nil || result.ExperimentName != "raytrain-tenant-a" {
		t.Fatalf("expected an empty, usable experiment response: result=%#v err=%v", result, err)
	}
}

func TestMLflowClientListsOwnerScopedTenantRunsWithoutArtifactFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/2.0/mlflow/experiments/get-by-name":
			if got := request.URL.Query().Get("experiment_name"); got != "raytrain-tenant-a" {
				t.Fatalf("unexpected experiment name %q", got)
			}
			_, _ = response.Write([]byte(`{"experiment":{"experiment_id":"7","artifact_location":"s3://private"}}`))
		case "/api/2.0/mlflow/runs/search":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if filter, _ := body["filter"].(string); filter != "tags.`platform.submitter_user_id` = 'user-a'" {
				t.Fatalf("engineer catalog must be owner scoped, got %q", filter)
			}
			if max, _ := body["max_results"].(float64); max != 25 {
				t.Fatalf("unexpected max_results %#v", body["max_results"])
			}
			payload := fmt.Sprintf(`{"runs":[{"info":{"run_id":"run-1","run_name":"train-one","status":"FINISHED","start_time":1000,"end_time":2500,"artifact_uri":"s3://private/run-1"},"data":{"metrics":[{"key":"loss","value":1.25},{"key":"mAP","value":0.61},{"key":"invalid_metric","value":"NaN"}],"params":[{"key":"epochs","value":"1"}],"tags":[{"key":"platform.job_id","value":"job-01"},{"key":"platform.submitter_user_id","value":"user-a"},{"key":"platform.provenance","value":"%s"},{"key":"secret","value":"must-not-leak"}]}}]}`, mlflowProvenanceTag(testProvenanceKey, "job-01"))
			_, _ = response.Write([]byte(payload))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: testProvenanceKey, HTTPClient: server.Client()}
	result, err := client.ListTenantExperiments(context.Background(), "tenant-a", "user-a", 25)
	if err != nil {
		t.Fatalf("list MLflow experiments: %v", err)
	}
	if result.ExperimentName != "raytrain-tenant-a" || len(result.Runs) != 1 {
		t.Fatalf("unexpected catalog: %#v", result)
	}
	run := result.Runs[0]
	if run.ID != "run-1" || run.JobID != "job-01" || run.SubmitterUserID != "user-a" || run.Latest["mAP"] != 0.61 {
		t.Fatalf("unexpected run: %#v", run)
	}
	if _, exists := run.Latest["invalid_metric"]; exists {
		t.Fatalf("non-finite MLflow metrics must be ignored: %#v", run.Latest)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"s3://", "must-not-leak", "artifact_uri", "epochs"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("experiment catalog leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestMLflowClientListsTenantAdminRunsWithoutOwnerFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/2.0/mlflow/experiments/get-by-name":
			_, _ = response.Write([]byte(`{"experiment":{"experiment_id":"7"}}`))
		case "/api/2.0/mlflow/runs/search":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if filter, exists := body["filter"]; exists && filter != "" {
				t.Fatalf("tenant admin catalog must not have an owner filter: %#v", body)
			}
			_, _ = response.Write([]byte(`{"runs":[]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "raytrain", ProvenanceKey: testProvenanceKey, HTTPClient: server.Client()}
	result, err := client.ListTenantExperiments(context.Background(), "tenant-a", "", 50)
	if err != nil || result.ExperimentName != "raytrain-tenant-a" || len(result.Runs) != 0 {
		t.Fatalf("unexpected tenant catalog: result=%#v err=%v", result, err)
	}
}

func TestMLflowClientRejectsUnsafeCatalogIdentityAndLimit(t *testing.T) {
	client := &MLflowClient{BaseURL: "http://mlflow.invalid", ExperimentPrefix: "raytrain", ProvenanceKey: testProvenanceKey}
	for _, test := range []struct {
		tenant  string
		subject string
		limit   int
	}{
		{tenant: "tenant/a", subject: "user-a", limit: 50},
		{tenant: "tenant-a", subject: "user' OR 1=1", limit: 50},
		{tenant: "tenant-a", subject: "user-a", limit: 0},
		{tenant: "tenant-a", subject: "user-a", limit: 101},
	} {
		if _, err := client.ListTenantExperiments(context.Background(), test.tenant, test.subject, test.limit); err == nil {
			t.Fatalf("expected unsafe catalog request rejection for %#v", test)
		}
	}
}
