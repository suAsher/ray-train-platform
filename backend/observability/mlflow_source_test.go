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

func jobSourceRun(id, experimentID, jobID, provenance string) map[string]any {
	info := map[string]any{"run_id": id}
	if experimentID != "" {
		info["experiment_id"] = experimentID
	}
	return map[string]any{
		"info": info,
		"data": map[string]any{"tags": []map[string]string{
			{"key": "platform.job_id", "value": jobID},
			{"key": "platform.provenance", "value": provenance},
		}},
	}
}

func TestResolveJobSourceRequiresUniqueVerifiedRun(t *testing.T) {
	proof := mlflowProvenanceTag(testProvenanceKey, "job-01")
	valid := jobSourceRun("run-1", "7", "job-01", proof)
	for _, test := range []struct {
		name string
		runs []map[string]any
		token string
		want error
	}{
		{name: "one", runs: []map[string]any{valid}},
		{name: "optional experiment omitted", runs: []map[string]any{jobSourceRun("run-1", "", "job-01", proof)}},
		{name: "none", want: ErrJobSourceUnavailable},
		{name: "multiple", runs: []map[string]any{valid, jobSourceRun("run-2", "7", "job-01", proof)}, want: ErrAmbiguousJobSource},
		{name: "next page", runs: []map[string]any{valid}, token: "more", want: ErrAmbiguousJobSource},
		{name: "empty page with next page", token: "more", want: ErrAmbiguousJobSource},
		{name: "empty run", runs: []map[string]any{jobSourceRun("", "7", "job-01", proof)}, want: ErrJobSourceUnavailable},
		{name: "unsafe run", runs: []map[string]any{jobSourceRun("run/../../other", "7", "job-01", proof)}, want: ErrJobSourceUnavailable},
		{name: "wrong experiment", runs: []map[string]any{jobSourceRun("run-1", "8", "job-01", proof)}, want: ErrJobSourceUnavailable},
		{name: "null experiment", runs: []map[string]any{{"info": map[string]any{"run_id": "run-1", "experiment_id": nil}, "data": valid["data"]}}, want: ErrJobSourceUnavailable},
		{name: "empty experiment", runs: []map[string]any{{"info": map[string]any{"run_id": "run-1", "experiment_id": ""}, "data": valid["data"]}}, want: ErrJobSourceUnavailable},
		{name: "numeric experiment", runs: []map[string]any{{"info": map[string]any{"run_id": "run-1", "experiment_id": 7}, "data": valid["data"]}}, want: ErrJobSourceUnavailable},
		{name: "wrong job", runs: []map[string]any{jobSourceRun("run-1", "7", "job-other", proof)}, want: ErrJobSourceUnavailable},
		{name: "invalid provenance", runs: []map[string]any{jobSourceRun("run-1", "7", "job-01", "forged")}, want: ErrJobSourceUnavailable},
		{name: "missing tags", runs: []map[string]any{{"info": map[string]string{"run_id": "run-1"}}}, want: ErrJobSourceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/api/2.0/mlflow/experiments/get-by-name":
					if r.Method != http.MethodGet || r.URL.Query().Get("experiment_name") != "custom-tenant-a" {
						t.Errorf("wrong tenant lookup: %s %s", r.Method, r.URL)
					}
					_, _ = w.Write([]byte(`{"experiment":{"experiment_id":"7"}}`))
				case "/api/2.0/mlflow/runs/search":
					var body struct {
						Experiments []string `json:"experiment_ids"`
						Filter string `json:"filter"`
						MaxResults int `json:"max_results"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					wantFilter := "tags.`platform.job_id` = 'job-01' AND tags.`platform.provenance` = '" + proof + "'"
					if r.Method != http.MethodPost || len(body.Experiments) != 1 || body.Experiments[0] != "7" || body.Filter != wantFilter || body.MaxResults != 2 {
						t.Errorf("unsafe source search: %#v", body)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"runs": test.runs, "next_page_token": test.token})
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: "-custom-", ProvenanceKey: testProvenanceKey, HTTPClient: server.Client()}
			got, err := client.ResolveJobSource(context.Background(), "tenant-a", "job-01")
			if !errors.Is(err, test.want) {
				t.Fatalf("source error = %v, want %v", err, test.want)
			}
			if err == nil && got != (JobSource{JobID: "job-01", RunID: "run-1", ExperimentID: "7"}) {
				t.Fatalf("source = %#v", got)
			}
			if err != nil && got != (JobSource{}) {
				t.Fatalf("failed source leaked partial association: %#v", got)
			}
			if calls != 2 {
				t.Fatalf("calls = %d, want exactly tenant lookup and run search", calls)
			}
		})
	}
}

func TestJobSourceRejectsDuplicateBindingTags(t *testing.T) {
	proof := mlflowProvenanceTag(testProvenanceKey, "job-01")
	for _, duplicate := range []map[string]string{
		{"key": "platform.job_id", "value": "job-01"},
		{"key": "platform.provenance", "value": proof},
		{"key": "platform.job_id", "value": "job-other"},
		{"key": "platform.provenance", "value": "forged"},
	} {
		payload := map[string]any{"data": map[string]any{"tags": []map[string]string{
			{"key": "platform.job_id", "value": "job-01"},
			{"key": "platform.provenance", "value": proof},
			duplicate,
		}}}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var run jobSourceRunResponse
		if err := json.Unmarshal(encoded, &run); err != nil {
			t.Fatal(err)
		}
		if run.verifiedTags("job-01", proof) {
			t.Fatalf("duplicate source tag accepted: %s", duplicate["key"])
		}
	}
}

func TestResolveJobSourceRejectsMissingOrMalformedExperiment(t *testing.T) {
	for _, test := range []struct {
		name string
		status int
		body string
	}{
		{"missing", 404, `{}`},
		{"no id", 200, `{"experiment":{}}`},
		{"invalid id", 200, `{"experiment":{"experiment_id":"../other"}}`},
		{"wrong JSON type", 200, `{"experiment":{"experiment_id":7}}`},
		{"upstream failure", 503, `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/2.0/mlflow/experiments/get-by-name" {
					t.Error("must not search an invalid experiment")
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client := &MLflowClient{BaseURL: server.URL, ProvenanceKey: testProvenanceKey, HTTPClient: server.Client()}
			got, err := client.ResolveJobSource(context.Background(), "tenant-a", "job-01")
			if !errors.Is(err, ErrJobSourceUnavailable) || got != (JobSource{}) {
				t.Fatalf("source = %#v, error = %v", got, err)
			}
		})
	}
}

func TestResolveJobSourceRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid source request reached MLflow")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, test := range []struct {
		name, prefix, tenant, job string
		key []byte
	}{
		{name: "no key", tenant: "tenant-a", job: "job-01"},
		{name: "short key", tenant: "tenant-a", job: "job-01", key: []byte("short")},
		{name: "unsafe tenant", tenant: "tenant'a", job: "job-01", key: testProvenanceKey},
		{name: "unsafe job", tenant: "tenant-a", job: "job' OR 1=1", key: testProvenanceKey},
		{name: "unsafe prefix", prefix: "prefix/", tenant: "tenant-a", job: "job-01", key: testProvenanceKey},
		{name: "overlong job", tenant: "tenant-a", job: strings.Repeat("a", 129), key: testProvenanceKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &MLflowClient{BaseURL: server.URL, ExperimentPrefix: test.prefix, ProvenanceKey: test.key, HTTPClient: server.Client()}
			if _, err := client.ResolveJobSource(context.Background(), test.tenant, test.job); !errors.Is(err, ErrJobSourceUnavailable) {
				t.Fatalf("invalid input error = %v", err)
			}
		})
	}
	var absent *MLflowClient
	if _, err := absent.ResolveJobSource(context.Background(), "tenant-a", "job-01"); !errors.Is(err, ErrJobSourceUnavailable) {
		t.Fatalf("nil client error = %v", err)
	}
}
