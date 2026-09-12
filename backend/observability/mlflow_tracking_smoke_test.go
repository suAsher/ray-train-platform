package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/mlflowtracking"
)

func TestMLflowTrackingProviderMLflow314Smoke(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("MLFLOW_TRACKING_SMOKE_URL"))
	if baseURL == "" {
		t.Skip("set MLFLOW_TRACKING_SMOKE_URL to an isolated MLflow 3.14 service URL to run this write smoke")
	}
	validateTrackingSmokeURL(t, baseURL)
	t.Log("expected isolated service image: harbor.wellspiking.ai/guofeng.su/mlflow:v3.14.0-full@sha256:03c206d175084ee3f654a1353ab62ccd2e59048c54f9488f08cc4d8f5de0037d in namespace mlflow-system")

	operationID := randomTrackingOperationID(t)
	key := []byte(strings.Repeat("s", 32))
	if override := strings.TrimSpace(os.Getenv("MLFLOW_TRACKING_SMOKE_PROVENANCE_KEY")); override != "" {
		key = []byte(override)
	}
	client := &MLflowClient{
		BaseURL:          baseURL,
		ExperimentPrefix: "raytrain-smoke",
		ProvenanceKey:    key,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	experimentID, err := client.CreateExperiment(ctx, operationID)
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	if experimentID == "" {
		t.Fatal("create experiment returned empty upstream id")
	}
	repeatedExperimentID, err := client.CreateExperiment(ctx, operationID)
	if err != nil {
		t.Fatalf("repeat create experiment: %v", err)
	}
	if repeatedExperimentID != experimentID {
		t.Fatalf("repeat create experiment returned %q, want %q", repeatedExperimentID, experimentID)
	}

	runID, err := client.CreateRun(ctx, experimentID, operationID, "provider-smoke")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if !mlflowIntegrationRunID.MatchString(runID) {
		t.Fatalf("create run returned invalid upstream id %q", runID)
	}
	repeatedRunID, err := client.CreateRun(ctx, experimentID, operationID, "provider-smoke")
	if err != nil {
		t.Fatalf("repeat create run: %v", err)
	}
	if repeatedRunID != runID {
		t.Fatalf("repeat create run returned %q, want %q", repeatedRunID, runID)
	}

	value := 0.91
	timestamp := time.Now().UTC().UnixMilli()
	step := int64(1)
	if err := client.LogRun(ctx, experimentID, runID, operationID, mlflowtracking.Batch{
		Metrics: []mlflowtracking.Metric{{Key: "smoke/accuracy", Value: &value, Timestamp: &timestamp, Step: &step}},
		Params:  []mlflowtracking.Pair{{Key: "smoke.param", Value: "ok"}},
		Tags:    []mlflowtracking.Pair{{Key: "smoke.tag", Value: "ok"}},
	}); err != nil {
		t.Fatalf("log run: %v", err)
	}

	beforeFinish, err := client.ReadRun(ctx, experimentID, runID, operationID)
	if err != nil {
		t.Fatalf("read run before finish: %v", err)
	}
	if beforeFinish.Status != "RUNNING" || beforeFinish.Latest["smoke/accuracy"] != value || beforeFinish.Params["smoke.param"] != "ok" {
		t.Fatalf("unexpected running snapshot %#v", beforeFinish)
	}

	if err := client.FinishRun(ctx, experimentID, runID, operationID, "FINISHED", time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if err := client.FinishRun(ctx, experimentID, runID, operationID, "FINISHED", time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("repeat finish run should be idempotent: %v", err)
	}
	afterFinish, err := client.ReadRun(ctx, experimentID, runID, operationID)
	if err != nil {
		t.Fatalf("read run after finish: %v", err)
	}
	if afterFinish.Status != "FINISHED" {
		t.Fatalf("unexpected finished snapshot %#v", afterFinish)
	}
}

func validateTrackingSmokeURL(t *testing.T, raw string) {
	t.Helper()
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "http" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" && target.Path != "/" {
		t.Fatal("MLflow tracking smoke URL must be an isolated HTTP origin without credentials, path, query or fragment")
	}
	switch target.Hostname() {
	case "tracking-mlflow", "localhost", "127.0.0.1":
	default:
		t.Fatal("MLflow tracking smoke tests are restricted to tracking-mlflow, localhost or 127.0.0.1")
	}
}

func randomTrackingOperationID(t *testing.T) string {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(bytes[:])
}
