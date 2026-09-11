package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in test creates records only in a disposable MLflow 3.14 service.
// Production hostnames are deliberately excluded even when an environment
// variable is accidentally copied from an application configuration.
func TestMLflowIntegrationRealServerSmoke(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("MLFLOW_INTEGRATION_TEST_URL"))
	if base == "" {
		t.Skip("MLFLOW_INTEGRATION_TEST_URL requires an isolated MLflow 3.14 service")
	}
	target, err := url.Parse(base)
	if err != nil || target.Scheme != "http" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		t.Fatal("invalid isolated MLflow test URL")
	}
	switch target.Hostname() {
	case "localhost", "127.0.0.1", "::1", "mlflow-integration":
	default:
		t.Fatal("MLflow smoke tests are restricted to loopback or the mlflow-integration test container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := &MLflowClient{BaseURL: base, ProvenanceKey: []byte(strings.Repeat("test-only-key-", 3)), HTTPClient: &http.Client{Timeout: 10 * time.Second}}
	verifySmokeMLflowVersion(t, ctx, client)
	tenant := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	job, owner := "smoke-job", "smoke-owner"
	var experiment struct {
		ID string `json:"experiment_id"`
	}
	smokeMLflowJSON(t, ctx, client, "/api/2.0/mlflow/experiments/create", map[string]any{"name": "raytrain-" + tenant}, &experiment)
	if experiment.ID == "" {
		t.Fatal("MLflow did not create the isolated experiment")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		endpoint, endpointErr := client.endpoint("/api/2.0/mlflow/experiments/delete")
		if endpointErr != nil {
			t.Error(endpointErr)
			return
		}
		var result map[string]any
		if _, deleteErr := client.doJSON(cleanupCtx, http.MethodPost, endpoint, map[string]any{"experiment_id": experiment.ID}, &result); deleteErr != nil {
			t.Errorf("clean isolated experiment: %v", deleteErr)
		}
	})
	baseTags := []MLflowKeyValue{{Key: "platform.job_id", Value: job}, {Key: "platform.provenance", Value: mlflowProvenanceTag(client.ProvenanceKey, job)}}
	oldRun := createSmokeMLflowRun(t, ctx, client, experiment.ID, baseTags, 1000)
	fullTags := append(append([]MLflowKeyValue{}, baseTags...), MLflowKeyValue{Key: "platform.tenant_id", Value: tenant}, MLflowKeyValue{Key: "platform.submitter_user_id", Value: owner})
	newRun := createSmokeMLflowRun(t, ctx, client, experiment.ID, fullTags, 2000)
	value, timestamp, step := 0.25, int64(3000), int64(1)
	for _, batch := range []MLflowLogBatch{
		{Params: []MLflowKeyValue{{Key: "epochs", Value: "5"}}},
		{Metrics: []MLflowLogMetric{{Key: "loss", Value: &value, Timestamp: &timestamp, Step: &step}}},
		{Tags: []MLflowKeyValue{{Key: "review", Value: "candidate"}}},
	} {
		if err := client.LogJobRunBatch(ctx, tenant, job, owner, newRun, batch); err != nil {
			t.Fatalf("real single-field batch: %v", err)
		}
	}
	result, err := client.QueryJobRun(ctx, tenant, job, owner, newRun)
	if err != nil || result.Run == nil || result.Run.Params["epochs"] != "5" || result.Run.Latest["loss"] != value || len(result.Series) != 1 || len(result.Series[0].Points) != 1 {
		t.Fatalf("real run readback: %+v %v", result, err)
	}
	var raw struct {
		Run mlflowIntegrationRun `json:"run"`
	}
	endpoint, _ := client.endpoint("/api/2.0/mlflow/runs/get")
	query := endpoint.Query()
	query.Set("run_id", newRun)
	endpoint.RawQuery = query.Encode()
	if _, err := client.doJSON(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
		t.Fatal(err)
	}
	tagFound := false
	for _, tag := range raw.Run.Data.Tags {
		if tag.Key == "review" && tag.Value == "candidate" {
			tagFound = true
		}
	}
	if !tagFound {
		t.Fatal("custom tag was not persisted")
	}
	older, err := client.QueryJobRun(ctx, tenant, job, owner, oldRun)
	if err != nil || older.Run == nil || older.Run.ID != oldRun || older.Run.ID == newRun {
		t.Fatalf("older legacy attempt was not read exactly: %+v %v", older, err)
	}
	if err := client.LogJobRunBatch(ctx, tenant, job, owner, oldRun, MLflowLogBatch{Tags: []MLflowKeyValue{{Key: "review", Value: "legacy"}}}); !errors.Is(err, ErrMLflowRunNotFound) {
		t.Fatalf("legacy run write was allowed: %v", err)
	}
	var updated map[string]any
	smokeMLflowJSON(t, ctx, client, "/api/2.0/mlflow/runs/update", map[string]any{"run_id": newRun, "status": "FINISHED", "end_time": 4000}, &updated)
	if err := client.LogJobRunBatch(ctx, tenant, job, owner, newRun, MLflowLogBatch{Tags: []MLflowKeyValue{{Key: "review", Value: "too-late"}}}); !errors.Is(err, ErrMLflowRunNotRunning) {
		t.Fatalf("terminal run write was allowed: %v", err)
	}
}

func createSmokeMLflowRun(t *testing.T, ctx context.Context, client *MLflowClient, experimentID string, tags []MLflowKeyValue, start int64) string {
	t.Helper()
	var response struct {
		Run mlflowIntegrationRun `json:"run"`
	}
	smokeMLflowJSON(t, ctx, client, "/api/2.0/mlflow/runs/create", map[string]any{"experiment_id": experimentID, "start_time": start, "tags": tags}, &response)
	if !mlflowIntegrationRunID.MatchString(response.Run.Info.ID) {
		t.Fatal("isolated run was not created")
	}
	return response.Run.Info.ID
}

func smokeMLflowJSON(t *testing.T, ctx context.Context, client *MLflowClient, path string, body, result any) {
	t.Helper()
	endpoint, err := client.endpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.doJSON(ctx, http.MethodPost, endpoint, body, result); err != nil {
		t.Fatalf("isolated MLflow %s: %v", path, err)
	}
}

func verifySmokeMLflowVersion(t *testing.T, ctx context.Context, client *MLflowClient) {
	t.Helper()
	endpoint, err := client.endpoint("/version")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	version, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(strings.TrimSpace(string(version)), "3.14.") {
		t.Fatalf("smoke test requires MLflow 3.14.x, status=%d version=%q error=%v", resp.StatusCode, version, err)
	}
}
