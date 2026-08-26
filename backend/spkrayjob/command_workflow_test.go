package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func testEnvironment(key string) string {
	if key == "RAY_PLATFORM_TOKEN" {
		return "test-token"
	}
	return ""
}

// artifactStubHandler answers the upload handshake every submission performs so
// a workflow test can focus on the job specification the CLI composes.
func artifactStubHandler(t *testing.T, onSubmit func(domain.JobSpec)) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			var create struct {
				SHA256    string `json:"sha256"`
				SizeBytes int64  `json:"sizeBytes"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": "artifact-test", "state": "PENDING", "sha256": create.SHA256, "sizeBytes": create.SizeBytes,
				"uploadUrl": serverURL(request) + "/upload", "contentLength": create.SizeBytes, "uploadRequired": true,
			})
		case "/upload":
			writer.WriteHeader(http.StatusOK)
		case "/api/v1/source-artifacts/artifact-test/complete":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": "artifact-test", "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			var body struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			onSubmit(body.Spec)
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}
}

func seedProject(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, projectFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// This is the "改代码即提交" loop: after the first setup, a submission needs no
// arguments at all.
func TestSubmitWithNoFlagsUsesTheCommittedProjectDefaults(t *testing.T) {
	root := seedProject(t, `name: bevfusion-lidar
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python tools/westwell_train.py configs/lidar.yaml --launcher pytorch
workers: 1
gpusPerWorker: 8
cpuPerWorker: 32
memoryPerWorker: 128Gi
executionMode: torchrun
input:
  space: public
  path: bevfusion/2026-08-0429
`)
	var submitted domain.JobSpec
	server := httptest.NewTLSServer(artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec }))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit with project defaults failed: %v (%s)", err, stdout.String())
	}
	if submitted.Name != "bevfusion-lidar" || submitted.Resources.GPUsPerWorker != 8 || submitted.Resources.WorkerReplicas != 1 {
		t.Fatalf("project defaults were not applied: %+v", submitted)
	}
	if submitted.Execution.Mode != domain.ExecutionModeTorchrun {
		t.Fatalf("expected single-node multi-GPU, got %q", submitted.Execution.Mode)
	}
	if submitted.Input.Space != domain.DataSpacePublic || submitted.Input.RelativePath != "bevfusion/2026-08-0429" {
		t.Fatalf("expected the committed input selection, got %+v", submitted.Input)
	}
	// The output directory defaults to the job name so results never land in a
	// shared folder by accident.
	if submitted.Output.Space != domain.DataSpaceMyRuns || submitted.Output.RelativePath != "bevfusion-lidar" {
		t.Fatalf("unexpected output selection: %+v", submitted.Output)
	}
	if submitted.Cache != (domain.CacheRequest{}) {
		t.Fatalf("the shortest submit path must keep cache off, got %+v", submitted.Cache)
	}
	if !strings.Contains(stdout.String(), "job-test") {
		t.Fatalf("expected a readable confirmation, got %q", stdout.String())
	}
}

func TestSubmitRuntimeCacheUsesIndependentFlagOverride(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
  size: 100Gi
`)
	var submitted domain.JobSpec
	limitsRead := false
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/limits" {
			limitsRead = true
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
				"enabled": true, "modes": []string{"off", "runtime"}, "allowedSizes": []string{"100Gi", "200Gi"},
				"defaultSize": "100Gi", "maxSize": "500Gi",
			}})
			return
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--cache-size", "200Gi",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit runtime cache: %v", err)
	}
	if submitted.Cache.Mode != domain.CacheModeRuntime || submitted.Cache.Size != "200Gi" {
		t.Fatalf("cache flag must independently override project size: %+v", submitted.Cache)
	}
	if !limitsRead {
		t.Fatal("runtime cache must be checked against authenticated platform limits")
	}
}

func TestSubmitCacheModeOffClearsInheritedSizeWithoutLimitsRequest(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
  size: 100Gi
`)
	var submitted domain.JobSpec
	limitsRead := false
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/limits" {
			limitsRead = true
			t.Fatal("cache mode off must not request platform limits")
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--cache-mode", "off",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit with cache disabled by flag: %v", err)
	}
	if limitsRead {
		t.Fatal("cache mode off must not request platform limits")
	}
	if submitted.Cache != (domain.CacheRequest{}) {
		t.Fatalf("cache mode off must omit cache from the job spec, got %+v", submitted.Cache)
	}
}

func TestSubmitExplicitRuntimeCacheFlags(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
`)
	var submitted domain.JobSpec
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/limits" {
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
				"enabled": true, "modes": []string{"off", "runtime"}, "allowedSizes": []string{"100Gi"},
				"defaultSize": "100Gi", "maxSize": "500Gi",
			}})
			return
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--cache-mode", "runtime", "--cache-size", "100Gi",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit explicit runtime cache flags: %v", err)
	}
	if submitted.Cache.Mode != domain.CacheModeRuntime || submitted.Cache.Size != "100Gi" {
		t.Fatalf("explicit cache flags were not applied: %+v", submitted.Cache)
	}
}

func TestSubmitAutomaticInputPreloadUsesOnlyUserFacingFlags(t *testing.T) {
	root := seedProject(t, `name: cache-preload
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
`)
	var submitted domain.JobSpec
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/limits" {
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
				"enabled": true, "modes": []string{"off", "runtime"}, "allowedSizes": []string{"1Ti"},
				"defaultSize": "1Ti", "maxSize": "5Ti",
			}})
			return
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--cache-mode", "runtime", "--cache-size", "1Ti", "--cache-preload", "input",
		"--input-space", "public", "--input-path", "labeled/fz-v1",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit automatic preload: %v", err)
	}
	if submitted.Cache != (domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "1Ti", Preload: domain.CachePreloadInput}) {
		t.Fatalf("automatic preload flags were not preserved: %+v", submitted.Cache)
	}
	if submitted.Input.Space != domain.DataSpacePublic || submitted.Input.RelativePath != "labeled/fz-v1" {
		t.Fatalf("selected input was not preserved: %+v", submitted.Input)
	}
}

func TestSubmitRuntimeCacheUsesServerDefaultBeforeUpload(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
`)
	limitsRead := false
	var submitted domain.JobSpec
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/limits" {
			limitsRead = true
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
				"enabled": true, "modes": []string{"off", "runtime"}, "allowedSizes": []string{"100Gi", "200Gi"},
				"defaultSize": "200Gi", "maxSize": "500Gi",
			}})
			return
		}
		if request.URL.Path == "/api/v1/source-artifacts" && !limitsRead {
			t.Fatal("limits must be resolved before source archive upload")
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit runtime cache with platform default: %v", err)
	}
	if submitted.Cache.Mode != domain.CacheModeRuntime || submitted.Cache.Size != "200Gi" {
		t.Fatalf("server default cache size was not applied: %+v", submitted.Cache)
	}
}

func TestSubmitRuntimeCacheWithoutServerDefaultFailsBeforeUpload(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
`)
	uploaded := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/limits" {
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
				"enabled": true, "modes": []string{"off", "runtime"}, "allowedSizes": []string{"100Gi"},
				"defaultSize": "", "maxSize": "500Gi",
			}})
			return
		}
		if request.URL.Path == "/api/v1/source-artifacts" || request.Method == http.MethodPut {
			uploaded = true
		}
		t.Fatalf("unexpected request before cache validation: %s %s", request.Method, request.URL.Path)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil || !strings.Contains(err.Error(), "默认容量") {
		t.Fatalf("expected missing default-size error, got %v", err)
	}
	if uploaded {
		t.Fatal("missing runtime cache default must fail before source upload")
	}
}

func TestSubmitRejectsInvalidCachePolicyBeforeSourceUpload(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		size         string
		enabled      bool
		modes        []string
		allowedSizes []string
		maxSize      string
		wantError    string
	}{
		{name: "off with size", mode: "off", size: "100Gi", enabled: true, modes: []string{"off", "runtime"}, allowedSizes: []string{"100Gi"}, maxSize: "500Gi", wantError: "关闭"},
		{name: "unknown mode", mode: "durable", enabled: true, modes: []string{"off", "runtime"}, allowedSizes: []string{"100Gi"}, maxSize: "500Gi", wantError: "不支持"},
		{name: "disabled", mode: "runtime", size: "100Gi", enabled: false, modes: []string{"off", "runtime"}, allowedSizes: []string{"100Gi"}, maxSize: "500Gi", wantError: "未启用"},
		{name: "runtime mode disallowed", mode: "runtime", size: "100Gi", enabled: true, modes: []string{"off"}, allowedSizes: []string{"100Gi"}, maxSize: "500Gi", wantError: "不支持 runtime"},
		{name: "size disallowed", mode: "runtime", size: "300Gi", enabled: true, modes: []string{"off", "runtime"}, allowedSizes: []string{"100Gi", "200Gi"}, maxSize: "500Gi", wantError: "允许范围"},
		{name: "size exceeds maximum", mode: "runtime", size: "1Ti", enabled: true, modes: []string{"off", "runtime"}, allowedSizes: []string{"100Gi", "1Ti"}, maxSize: "500Gi", wantError: "超过"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: `+test.mode+`
  size: `+test.size+`
`)
			uploaded := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v1/limits" {
					writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
						"enabled": test.enabled, "modes": test.modes, "allowedSizes": test.allowedSizes,
						"defaultSize": "100Gi", "maxSize": test.maxSize,
					}})
					return
				}
				if request.URL.Path == "/api/v1/source-artifacts" || request.Method == http.MethodPut {
					uploaded = true
				}
				t.Fatalf("unexpected request before cache validation: %s %s", request.Method, request.URL.Path)
			}))
			defer server.Close()

			err := Run(context.Background(), []string{
				"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
			}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected cache error containing %q, got %v", test.wantError, err)
			}
			if uploaded {
				t.Fatal("invalid cache request must fail before source archive upload")
			}
		})
	}
}

func TestSubmitOffCacheWithSizeFailsBeforeClientConfiguration(t *testing.T) {
	for _, arguments := range [][]string{
		{"--cache-size", "100Gi"},
		{"--cache-mode", "off", "--cache-size", "100Gi"},
	} {
		root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
`)
		command := append([]string{"submit", "--dir", root}, arguments...)
		err := Run(context.Background(), command, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" })
		if err == nil || !strings.Contains(err.Error(), "关闭") {
			t.Fatalf("off cache with arguments %v must fail locally before client configuration, got %v", arguments, err)
		}
	}
}

func TestSubmitFlagOverridesTheCommittedDefaultForOneRun(t *testing.T) {
	root := seedProject(t, `name: bevfusion-lidar
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python tools/westwell_train.py configs/lidar.yaml --launcher pytorch
workers: 1
gpusPerWorker: 8
executionMode: torchrun
`)
	var submitted domain.JobSpec
	server := httptest.NewTLSServer(artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec }))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--name", "quick-check", "--gpus-per-worker", "1", "--execution-mode", "single_gpu",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit with overrides failed: %v", err)
	}
	if submitted.Name != "quick-check" || submitted.Resources.GPUsPerWorker != 1 {
		t.Fatalf("flags must override the project file: %+v", submitted)
	}
	if submitted.Execution.Mode != domain.ExecutionModeSingleGPU {
		t.Fatalf("expected single_gpu, got %q", submitted.Execution.Mode)
	}
	if submitted.Entrypoint.Command[2] != "python tools/westwell_train.py configs/lidar.yaml --launcher pytorch" {
		t.Fatalf("untouched values must survive: %+v", submitted.Entrypoint)
	}
}

// Resuming reads the previous run's own managed result directory. The user
// supplies one job ID instead of reconstructing a storage path.
func TestSubmitResumeFromJobSelectsThePreviousRunAsReadOnlyCheckpoint(t *testing.T) {
	root := seedProject(t, `name: bevfusion-lidar
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python tools/westwell_train.py configs/lidar.yaml --launcher pytorch --auto-resume
workers: 1
gpusPerWorker: 8
executionMode: torchrun
`)
	var submitted domain.JobSpec
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/jobs/job-previous" {
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"id": "job-previous", "observedState": "FAILED",
				"spec": map[string]any{"output": map[string]any{"space": "my-runs", "relativePath": "bevfusion-lidar"}},
			})
			return
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--resume-from-job", "job-previous",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("resume submit failed: %v", err)
	}
	if submitted.Checkpoint.Space != domain.DataSpaceMyRuns || submitted.Checkpoint.RelativePath != "bevfusion-lidar/job-previous" {
		t.Fatalf("expected the previous run directory as checkpoint, got %+v", submitted.Checkpoint)
	}
}

func TestJobsCommandPrintsAReadableTableByDefault(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api/v1/jobs") {
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
		if got := request.URL.Query().Get("status"); got != "RUNNING" {
			t.Fatalf("expected the state filter to reach the server, got %q", got)
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{
			"items": []any{map[string]any{
				"id": "job-1", "observedState": "RUNNING", "submissionOrigin": "ray-cli",
				"spec": map[string]any{"name": "bevfusion", "resources": map[string]any{"workerReplicas": 1, "gpusPerWorker": 8}},
			}},
			"total": 1,
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"jobs", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--state", "running"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("jobs failed: %v", err)
	}
	for _, expected := range []string{"JOB ID", "job-1", "bevfusion", "RUNNING"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("expected %q in:\n%s", expected, stdout.String())
		}
	}
}

// Scripts and the existing e2e contract depend on machine-readable output, so
// the JSON escape hatch must survive the readability change.
func TestStatusOutputJSONKeepsTheRawEnvelopeForScripts(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"id": "job-1", "observedState": "SUCCEEDED"})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"status", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--output", "json", "job-1"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &decoded); err != nil {
		t.Fatalf("--output json must emit parseable JSON, got %q", stdout.String())
	}
	if decoded["observedState"] != "SUCCEEDED" {
		t.Fatalf("unexpected payload: %v", decoded)
	}
}

func TestLogsFollowStopsWhenTheJobReachesATerminalState(t *testing.T) {
	logCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/logs"):
			logCalls++
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{
				map[string]any{"timestamp": "2026-08-19T02:03:0" + string(rune('0'+logCalls)) + "Z", "line": "line " + string(rune('0'+logCalls))},
			}})
		case strings.HasSuffix(request.URL.Path, "job-1"):
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"id": "job-1", "observedState": "SUCCEEDED"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"logs", "-f", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("logs --follow failed: %v", err)
	}
	// A terminal job triggers one final drain pass, so the tail of a finished
	// run is never truncated.
	if logCalls != 2 {
		t.Fatalf("expected a final drain pass after the terminal state, got %d log calls", logCalls)
	}
	if !strings.Contains(stdout.String(), "line 1") || !strings.Contains(stdout.String(), "line 2") {
		t.Fatalf("unexpected follow output:\n%s", stdout.String())
	}
}

func TestLogsCommandPaginatesCompleteHistoryByDefault(t *testing.T) {
	logCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/logs") {
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
		logCalls++
		if request.URL.Query().Get("direction") != "forward" {
			t.Fatalf("logs history must page forward: %s", request.URL.RawQuery)
		}
		switch logCalls {
		case 1:
			if request.URL.Query().Get("after") != "" {
				t.Fatalf("first page unexpectedly had a cursor: %s", request.URL.RawQuery)
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"items": []any{
					map[string]any{"timestamp": "2026-08-22T16:00:01Z", "line": "line one"},
					map[string]any{"timestamp": "2026-08-22T16:00:02Z", "line": "line two"},
				},
				"page": map[string]any{"direction": "forward", "limit": 4000, "hasMore": true, "nextCursor": "2026-08-22T16:00:02Z"},
			})
		case 2:
			if request.URL.Query().Get("after") != "2026-08-22T16:00:02Z" {
				t.Fatalf("second page cursor missing: %s", request.URL.RawQuery)
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"items": []any{map[string]any{"timestamp": "2026-08-22T16:00:03Z", "line": "final success"}},
				"page":  map[string]any{"direction": "forward", "limit": 4000, "hasMore": false, "nextCursor": "2026-08-22T16:00:03Z"},
			})
		default:
			t.Fatalf("unexpected log page %d", logCalls)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"logs", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("logs failed: %v", err)
	}
	if logCalls != 2 || stdout.String() != "line one\nline two\nfinal success\n" {
		t.Fatalf("calls=%d output=%q", logCalls, stdout.String())
	}
}

func TestLogsCommandRejectsLegacyResponseForCompleteHistory(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{
			"items": []any{map[string]any{"timestamp": "2026-08-22T16:00:01Z", "line": "partial"}},
		})
	}))
	defer server.Close()

	err := Run(context.Background(), []string{"logs", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil || !strings.Contains(err.Error(), "does not support complete log pagination") {
		t.Fatalf("expected rolling-upgrade pagination error, got %v", err)
	}
	err = Run(context.Background(), []string{"logs", "--limit", "5000", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil || !strings.Contains(err.Error(), "does not support complete log pagination") {
		t.Fatalf("expected explicit-limit rolling-upgrade error, got %v", err)
	}
}

func TestLogsFollowStartsAfterTheInitialTailAndDrainsEveryFinalPage(t *testing.T) {
	logCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/logs"):
			logCalls++
			stamp := "2026-08-22T16:00:01Z"
			switch logCalls {
			case 1:
				writeClientSuccess(t, writer, http.StatusOK, map[string]any{
					"items": []any{map[string]any{"timestamp": stamp, "line": "same-time one"}},
					"page":  map[string]any{"direction": "backward", "limit": 1000, "hasMore": false, "nextCursor": stamp + "~1"},
				})
			case 2:
				if request.URL.Query().Get("after") != "2026-08-22T16:00:01.000000001Z" {
					t.Fatalf("follow cursor missing: %s", request.URL.RawQuery)
				}
				writeClientSuccess(t, writer, http.StatusOK, map[string]any{
					"items": []any{map[string]any{"timestamp": "2026-08-22T16:00:02Z", "line": "next line"}},
					"page":  map[string]any{"direction": "forward", "limit": 1000, "hasMore": true, "nextCursor": "2026-08-22T16:00:02Z~1"},
				})
			case 3:
				writeClientSuccess(t, writer, http.StatusOK, map[string]any{
					"items": []any{map[string]any{"timestamp": "2026-08-22T16:00:03Z", "line": "final success"}},
					"page":  map[string]any{"direction": "forward", "limit": 1000, "hasMore": false, "nextCursor": "2026-08-22T16:00:03Z~1"},
				})
			default:
				t.Fatalf("unexpected log call %d", logCalls)
			}
		case strings.HasSuffix(request.URL.Path, "job-1"):
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"id": "job-1", "observedState": "SUCCEEDED"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"logs", "-f", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if logCalls != 3 || stdout.String() != "same-time one\nnext line\nfinal success\n" {
		t.Fatalf("calls=%d output=%q", logCalls, stdout.String())
	}
}

func TestLogsFollowRejectsARepeatedBacklogCursor(t *testing.T) {
	logCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/logs"):
			logCalls++
			stamp := "2026-08-22T16:00:01Z"
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"items": []any{map[string]any{"timestamp": stamp, "line": fmt.Sprintf("line %d", logCalls)}},
				"page": map[string]any{
					"direction": map[bool]string{true: "backward", false: "forward"}[logCalls == 1],
					"limit":     1000, "hasMore": logCalls > 1, "nextCursor": stamp + "~1",
				},
			})
		case strings.HasSuffix(request.URL.Path, "job-1"):
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"id": "job-1", "observedState": "SUCCEEDED"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), []string{"logs", "-f", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil || !strings.Contains(err.Error(), "cursor did not advance") {
		t.Fatalf("expected repeated cursor rejection, got %v", err)
	}
}

func TestLogsJSONReportsMoreHistoryWhenLimitTruncates(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{
			"jobId": "job-1",
			"items": []any{map[string]any{"timestamp": "2026-08-22T16:00:01Z", "line": "first"}},
			"page":  map[string]any{"direction": "forward", "limit": 1, "hasMore": true, "nextCursor": "2026-08-22T16:00:01Z~1"},
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"logs", "--limit", "1", "--output", "json", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-1"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	var payload LogPage
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Page.HasMore || payload.Page.NextCursor != "2026-08-22T16:00:01Z~1" {
		t.Fatalf("truncation metadata was lost: %+v", payload.Page)
	}
}

func TestInitWritesAProjectFileThatSubmitCanUse(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"init", "--dir", root, "--name", "bevfusion-lidar", "--gpus-per-worker", "8"},
		&stdout, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	loaded, err := loadProject(root)
	if err != nil {
		t.Fatalf("load generated project: %v", err)
	}
	if loaded.Name != "bevfusion-lidar" || loaded.GPUsPerWorker != 8 || loaded.ExecutionMode != string(domain.ExecutionModeTorchrun) {
		t.Fatalf("init produced unusable defaults: %+v", loaded)
	}
}

// Failing before the archive is built and uploaded keeps a misconfigured
// directory from consuming the tenant's pending-artifact quota. Reading the
// image catalogue is a cheap GET and is how --image stops being mandatory;
// what must not happen is an upload.
func TestSubmitWithoutAResolvableImageFailsBeforeUploading(t *testing.T) {
	root := t.TempDir()
	var uploaded bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "source-artifacts") || request.Method == http.MethodPut {
			uploaded = true
			t.Errorf("submit must not upload before the job values resolve: %s %s", request.Method, request.URL.Path)
		}
		// An empty catalogue leaves the image unresolvable.
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer server.Close()

	err := Run(context.Background(), []string{"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root, "--name", "x", "--entrypoint", "python train.py"},
		&bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil {
		t.Fatal("expected an error when no image can be resolved")
	}
	if !strings.Contains(err.Error(), "镜像") {
		t.Fatalf("expected a message about the image catalogue, got %v", err)
	}
	if uploaded {
		t.Fatal("the archive must not be uploaded")
	}
}

// The entrypoint is the only value that cannot be derived, so it must fail
// locally without any network call at all.
func TestSubmitWithoutEntrypointFailsWithoutContactingThePlatform(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("submit must not contact the platform when the entrypoint is missing")
	}))
	defer server.Close()

	err := Run(context.Background(), []string{"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--name", "x", "--image", "registry/i@sha256:" + strings.Repeat("a", 64)},
		&bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil || !strings.Contains(err.Error(), "--entrypoint") {
		t.Fatalf("expected a clear message naming the missing entrypoint, got %v", err)
	}
}

func TestSubmitRuntimeCacheWithoutEntrypointFailsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
cache:
  mode: runtime
`)
	getenv := func(key string) string {
		t.Fatalf("local validation must not read credentials or connection settings: %s", key)
		return ""
	}

	err := Run(context.Background(), []string{"submit", "--dir", root},
		&bytes.Buffer{}, &bytes.Buffer{}, getenv)
	if err == nil || !strings.Contains(err.Error(), "--entrypoint") {
		t.Fatalf("expected the original missing-entrypoint error, got %v", err)
	}
}

func TestSubmitRuntimeCacheResumeConflictFailsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
`)
	getenv := func(key string) string {
		t.Fatalf("local validation must not read credentials or connection settings: %s", key)
		return ""
	}

	err := Run(context.Background(), []string{
		"submit", "--dir", root, "--resume-from-job", "job-previous", "--checkpoint-path", "checkpoint",
	}, &bytes.Buffer{}, &bytes.Buffer{}, getenv)
	want := "--resume-from-job cannot be combined with --checkpoint-space or --checkpoint-path"
	if err == nil || err.Error() != want {
		t.Fatalf("expected the original resume/checkpoint conflict error %q, got %v", want, err)
	}
}

func TestSubmitRuntimeCacheInvalidExecutionModeFailsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
executionMode: invalid
cache:
  mode: runtime
  size: 100Gi
`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("local validation must not contact the platform")
	}))
	defer server.Close()
	getenv := func(key string) string {
		t.Fatalf("local validation must not read credentials or connection settings: %s", key)
		return ""
	}

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, getenv)
	if err == nil || !strings.Contains(err.Error(), `unsupported execution mode "invalid"`) {
		t.Fatalf("expected the invalid execution mode error, got %v", err)
	}
}

func TestSubmitRuntimeCacheMalformedDataLocationFailsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
input:
  space: public
  path: ../secret
cache:
  mode: runtime
  size: 100Gi
`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("local validation must not contact the platform")
	}))
	defer server.Close()
	getenv := func(key string) string {
		t.Fatalf("local validation must not read credentials or connection settings: %s", key)
		return ""
	}

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, getenv)
	if err == nil || !strings.Contains(err.Error(), "storage path contains an unsafe segment") {
		t.Fatalf("expected the malformed data location error, got %v", err)
	}
}

func TestSubmitRuntimeCacheInvalidArchiveFailsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
  size: 100Gi
`)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("local validation must not contact the platform")
	}))
	defer server.Close()
	getenv := func(key string) string {
		t.Fatalf("local validation must not read credentials or connection settings: %s", key)
		return ""
	}

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, getenv)
	if err == nil || !strings.Contains(err.Error(), "source symlink escapes source directory") {
		t.Fatalf("expected the invalid archive error, got %v", err)
	}
}

func TestSubmitRuntimeCacheMalformedSizeFailsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: cache-training
image: harbor.example/train@sha256:`+strings.Repeat("a", 64)+`
entrypoint: python train.py
cache:
  mode: runtime
  size: definitely-not-a-size
`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("local validation must not contact the platform")
	}))
	defer server.Close()
	getenv := func(key string) string {
		t.Fatalf("local validation must not read credentials or connection settings: %s", key)
		return ""
	}

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, getenv)
	if err == nil || !strings.Contains(err.Error(), "Kubernetes") {
		t.Fatalf("expected the malformed cache size error, got %v", err)
	}
}
