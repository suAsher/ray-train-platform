package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// This opt-in test runs the real MLflow 3.14 SDK against the native gateway
// and the isolated rtp-native-mlflow-test container. The upstream allowlist is
// intentionally narrow so this cannot be pointed at production MLflow.
func TestNativeMLflowSDK314FullAccessSmoke(t *testing.T) {
	upstream := firstNonEmptyEnv("MLFLOW_NATIVE_SMOKE_UPSTREAM_URL", "MLFLOW_NATIVE_UPSTREAM_URL")
	python := firstNonEmptyEnv("MLFLOW_NATIVE_SMOKE_PYTHON", "MLFLOW_SDK_SMOKE_PYTHON")
	if upstream == "" || python == "" {
		t.Skip("isolated native MLflow upstream and Python SDK are required")
	}
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "rtp-native-mlflow-test" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimRight(parsed.Path, "/") != "/mlflow" {
		t.Fatal("native MLflow smoke requires http://rtp-native-mlflow-test:5000/mlflow")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	directExperiment := "native-direct-" + suffix
	if _, err := createNativeSmokeExperiment(context.Background(), upstream, directExperiment); err != nil {
		t.Fatalf("create direct isolated experiment: %v", err)
	}

	const token = "native-smoke-token"
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+token {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("ray-platform-principal", nativeMLflowFullPrincipal())
		c.Next()
	})
	handler := NewHandler(&fakeJobRepository{}, Options{
		MLflowDashboardEnabled: true,
		MLflowDashboardStore:   newFakeMLflowDashboardStore(),
		MLflowTrackingURL:      upstream,
	})
	handler.RegisterMLflowNativeRoutes(router.Group("/api/v1"))
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", nativeMLflowSDKSmokePython, server.URL+"/api/v1/mlflow-native", directExperiment, suffix)
	cmd.Env = append(os.Environ(),
		"MLFLOW_TRACKING_TOKEN="+token,
		"MLFLOW_ENABLE_ASYNC_LOGGING=false",
		"MLFLOW_HTTP_REQUEST_MAX_RETRIES=0",
		"MLFLOW_ENABLE_TELEMETRY=false",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native MLflow SDK smoke failed: %v %s", err, strings.ReplaceAll(string(output), token, "[redacted]"))
	}
	t.Log(strings.TrimSpace(string(output)))
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func createNativeSmokeExperiment(ctx context.Context, upstream, name string) (string, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(upstream, "/")+"/api/2.0/mlflow/experiments/create", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var payload struct {
		ExperimentID string `json:"experiment_id"`
		Message      string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK || payload.ExperimentID == "" {
		return "", fmt.Errorf("status=%d message=%q", response.StatusCode, payload.Message)
	}
	return payload.ExperimentID, nil
}

const nativeMLflowSDKSmokePython = `
import hashlib
import json
import os
import pathlib
import sys
import tempfile
import time
import urllib.parse

import mlflow
from mlflow import MlflowClient
from mlflow.entities import ViewType

assert mlflow.__version__ == "3.14.0", mlflow.__version__

tracking_uri, direct_experiment, suffix = sys.argv[1:4]
client = MlflowClient(tracking_uri=tracking_uri)

all_experiments = client.search_experiments(view_type=ViewType.ALL, max_results=1000)
assert any(exp.name == direct_experiment for exp in all_experiments), direct_experiment
first_page = client.search_experiments(view_type=ViewType.ALL, max_results=1)
assert len(first_page) == 1, len(first_page)
assert getattr(first_page, "token", None), "expected pagination token from isolated default+direct experiments"

experiment_name = "native-proxy-" + suffix
experiment_id = client.create_experiment(experiment_name)
run = client.create_run(experiment_id, tags={"purpose": "native-smoke"})
run_id = run.info.run_id

client.log_param(run_id, "epochs", "3")
client.log_metric(run_id, "loss", 0.5, timestamp=1000, step=1)
client.log_metric(run_id, "loss", 0.25, timestamp=2000, step=2)
client.set_tag(run_id, "review", "candidate")
history = client.get_metric_history(run_id, "loss")
assert [(metric.value, metric.timestamp, metric.step) for metric in history] == [(0.5, 1000, 1), (0.25, 2000, 2)], history

artifact_root = pathlib.Path(tempfile.mkdtemp(prefix="native-mlflow-"))
artifact = artifact_root / "model.bin"
artifact_bytes = b"\x00native-artifact-registry\xff"
artifact.write_bytes(artifact_bytes)
client.log_artifact(run_id, str(artifact), artifact_path="weights")
downloaded = pathlib.Path(client.download_artifacts(run_id, "weights/model.bin"))
assert hashlib.sha256(downloaded.read_bytes()).hexdigest() == hashlib.sha256(artifact_bytes).hexdigest()

client.set_terminated(run_id, status="FINISHED", end_time=3000)
finished = client.get_run(run_id)
assert finished.info.status == "FINISHED", finished.info.status
assert finished.info.end_time == 3000, finished.info.end_time
assert finished.data.params["epochs"] == "3"
assert finished.data.metrics["loss"] == 0.25
assert finished.data.tags["review"] == "candidate"

model_name = "native-registry-" + suffix
client.create_registered_model(model_name)
source = finished.info.artifact_uri.rstrip("/") + "/weights/model.bin"
version = client.create_model_version(model_name, source, run_id=run_id)
deadline = time.time() + 30
while version.status == "PENDING_REGISTRATION" and time.time() < deadline:
    time.sleep(1)
    version = client.get_model_version(model_name, version.version)
assert version.name == model_name
assert version.run_id == run_id
client.set_registered_model_alias(model_name, "smoke", version.version)
aliased = client.get_model_version_by_alias(model_name, "smoke")
assert aliased.version == version.version
client.delete_registered_model_alias(model_name, "smoke")
client.delete_model_version(model_name, version.version)
client.delete_registered_model(model_name)

client.delete_run(run_id)
client.delete_experiment(experiment_id)

print(json.dumps({
    "sdk": mlflow.__version__,
    "directExperimentVisible": direct_experiment,
    "runId": run_id,
    "artifactSha256": hashlib.sha256(artifact_bytes).hexdigest(),
    "modelRegistry": model_name,
}, sort_keys=True))
`
