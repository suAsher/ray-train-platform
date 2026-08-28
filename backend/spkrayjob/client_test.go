package spkrayjob

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestSubmitDirectoryCreatesUploadsCompletesThenSubmits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	steps := make([]string, 0, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			steps = append(steps, "create")
			if request.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatal("API token missing")
			}
			var create map[string]any
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			if create["sha256"] == "" || create["sizeBytes"].(float64) < 1 {
				t.Fatalf("invalid create body=%v", create)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": "artifact-test", "state": "PENDING", "sha256": create["sha256"], "sizeBytes": create["sizeBytes"],
				"uploadUrl": serverURL(request) + "/upload?X-Tos-Signature=secret-signature", "requiredHeaders": map[string]string{"X-Required": "signed-value"},
				"contentLength": create["sizeBytes"], "uploadRequired": true,
			})
		case "/upload":
			steps = append(steps, "upload")
			if request.Header.Get("Authorization") != "" || request.Header.Get("X-Required") != "signed-value" {
				t.Fatal("direct upload headers were incorrect")
			}
			if _, err := io.ReadAll(request.Body); err != nil {
				t.Fatal(err)
			}
			writer.WriteHeader(http.StatusOK)
		case "/api/v1/source-artifacts/artifact-test/complete":
			steps = append(steps, "complete")
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": "artifact-test", "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			steps = append(steps, "submit")
			var submit struct {
				Spec   domain.JobSpec          `json:"spec"`
				Origin domain.SubmissionOrigin `json:"origin"`
			}
			if err := json.NewDecoder(request.Body).Decode(&submit); err != nil {
				t.Fatal(err)
			}
			if submit.Spec.Source.Type != "workspace-archive" || submit.Spec.Source.ArtifactID != "artifact-test" || submit.Origin != domain.SubmissionOriginRayCLI {
				t.Fatalf("job source=%+v", submit.Spec.Source)
			}
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.SubmitDirectory(context.Background(), root, testJobSpec())
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job-test" || !reflect.DeepEqual(steps, []string{"create", "upload", "complete", "submit"}) {
		t.Fatalf("job=%+v steps=%v", job, steps)
	}
}

func TestSubmitDirectoryRetriesLostArtifactCreateResponseWithStableID(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const artifactID = "artifact-0123456789abcdef01234567"
	createRequests := 0
	jobRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			createRequests++
			var create struct {
				ArtifactID string `json:"artifactId"`
				SHA256     string `json:"sha256"`
				SizeBytes  int64  `json:"sizeBytes"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			if create.ArtifactID != artifactID || create.SHA256 == "" || create.SizeBytes < 1 {
				t.Fatalf("unstable create request: %+v", create)
			}
			if createRequests == 1 {
				http.Error(writer, "response was lost", http.StatusServiceUnavailable)
				return
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": artifactID, "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			jobRequests++
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.SubmitDirectoryWithArtifactID(context.Background(), root, testJobSpec(), artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job-test" || createRequests != 2 || jobRequests != 1 {
		t.Fatalf("job=%+v createRequests=%d jobRequests=%d", job, createRequests, jobRequests)
	}
}

func TestSubmitDirectoryUploadFailureLeavesStableArtifactAndNoJob(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const artifactID = "artifact-fedcba987654321001234567"
	jobRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			var create struct {
				ArtifactID string `json:"artifactId"`
				SizeBytes  int64  `json:"sizeBytes"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			if create.ArtifactID != artifactID {
				t.Fatalf("artifactID=%q", create.ArtifactID)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": artifactID, "state": "PENDING", "uploadRequired": true,
				"uploadUrl": serverURL(request) + "/upload", "contentLength": create.SizeBytes,
			})
		case "/upload":
			http.Error(writer, "upload failed", http.StatusServiceUnavailable)
		case "/api/v1/jobs":
			jobRequests++
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SubmitDirectoryWithArtifactID(context.Background(), root, testJobSpec(), artifactID)
	if err == nil {
		t.Fatal("upload failure unexpectedly submitted a job")
	}
	if jobRequests != 0 {
		t.Fatalf("upload failure created %d jobs", jobRequests)
	}
}

func TestSubmitDirectoryManagedPreflightsImageBeforeBuildingArchive(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	artifactRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/limits":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"runtime": map[string]any{
				"availableEngines": []string{"ray-ddp", "ray-train"}, "managedEnabled": true,
				"productionRayVersion": domain.RayVersionProduction, "canaryRayVersion": domain.RayVersionCanary,
			}})
		case "/api/v1/images":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "Legacy", "reference": "registry/legacy:2.35", "rayVersion": domain.RayVersionLegacy,
				"supportedEngines": []string{"ray-ddp"},
			}})
		default:
			artifactRequests++
			t.Fatalf("managed preflight must finish before artifact work: %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	spec := testJobSpec()
	spec.Image = "registry/legacy:2.35"
	spec.TrainingEngine = domain.TrainingEngineRayTrain
	spec.Managed.MaxFailures = 2
	_, err = client.SubmitDirectory(context.Background(), root, spec)
	if err == nil || !strings.Contains(err.Error(), "ray-train") {
		t.Fatalf("expected image compatibility error before broken archive, got %v", err)
	}
	if artifactRequests != 0 {
		t.Fatalf("artifact work started %d times", artifactRequests)
	}
}

func TestSubmitManagedPreflightsImageBeforeCreatingJob(t *testing.T) {
	jobRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/limits":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"runtime": map[string]any{
				"availableEngines": []string{"ray-ddp", "ray-train"}, "managedEnabled": true,
				"productionRayVersion": domain.RayVersionProduction, "canaryRayVersion": domain.RayVersionCanary,
			}})
		case "/api/v1/images":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "Legacy", "reference": "registry/legacy:2.35", "rayVersion": domain.RayVersionLegacy,
				"supportedEngines": []string{"ray-ddp"},
			}})
		case "/api/v1/jobs":
			jobRequests++
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "must-not-submit"})
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	spec := testJobSpec()
	spec.Source = domain.CodeSource{Type: "workspace-archive", ArtifactID: "artifact-ready"}
	spec.Image = "registry/legacy:2.35"
	spec.TrainingEngine = domain.TrainingEngineRayTrain
	spec.Managed.MaxFailures = 2
	_, err = client.Submit(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "ray-train") {
		t.Fatalf("expected image compatibility error before job creation, got %v", err)
	}
	if jobRequests != 0 {
		t.Fatalf("job creation started %d times", jobRequests)
	}
}

func TestSubmitDirectoryRejectsInvalidManagedSpecBeforeNetworkOrArchive(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.JobSpec)
		wantError string
	}{
		{
			name: "resources",
			mutate: func(spec *domain.JobSpec) {
				spec.Resources.CPUPerWorker = 0
			},
			wantError: "cpuPerWorker must be positive",
		},
		{
			name: "timeout",
			mutate: func(spec *domain.JobSpec) {
				spec.TimeoutSeconds = -1
			},
			wantError: "timeoutSeconds must not be negative",
		},
		{
			name: "checkpoint frequency overflow",
			mutate: func(spec *domain.JobSpec) {
				spec.Managed.Checkpoint.EveryEpochs = 100001
			},
			wantError: "everyEpochs",
		},
		{
			name: "latest checkpoint retention overflow",
			mutate: func(spec *domain.JobSpec) {
				spec.Managed.Checkpoint.KeepLatest = 1001
			},
			wantError: "keepLatest",
		},
		{
			name: "best checkpoint retention overflow",
			mutate: func(spec *domain.JobSpec) {
				spec.Managed.Checkpoint.KeepBest = 1001
			},
			wantError: "keepBest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, "broken-link")); err != nil {
				t.Fatal(err)
			}
			requests := 0
			client, err := NewClient(ClientOptions{
				ServerURL: "https://platform.invalid",
				Token:     "test-token",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					return nil, errors.New("unexpected request")
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			spec := testJobSpec()
			spec.TrainingEngine = domain.TrainingEngineRayTrain
			spec.Managed.MaxFailures = 2
			test.mutate(&spec)
			original := spec

			_, err = client.SubmitDirectory(context.Background(), root, spec)
			if requests != 0 {
				t.Fatalf("invalid managed directory submit made %d HTTP requests", requests)
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected local validation error containing %q, got %v", test.wantError, err)
			}
			if !reflect.DeepEqual(spec, original) {
				t.Fatalf("directory submit mutated the caller's spec: before=%+v after=%+v", original, spec)
			}
		})
	}
}

func TestSubmitRejectsInvalidManagedSpecsBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.JobSpec)
		wantError string
	}{
		{
			name: "resources",
			mutate: func(spec *domain.JobSpec) {
				spec.Resources.CPUPerWorker = 0
			},
			wantError: "cpuPerWorker must be positive",
		},
		{
			name: "timeout",
			mutate: func(spec *domain.JobSpec) {
				spec.TimeoutSeconds = -1
			},
			wantError: "timeoutSeconds must not be negative",
		},
		{
			name: "engine",
			mutate: func(spec *domain.JobSpec) {
				spec.TrainingEngine = domain.TrainingEngine("ray-unknown")
			},
			wantError: "unsupported training engine",
		},
		{
			name: "checkpoint frequency overflow",
			mutate: func(spec *domain.JobSpec) {
				spec.Managed.Checkpoint.EveryEpochs = 100001
			},
			wantError: "everyEpochs",
		},
		{
			name: "latest checkpoint retention overflow",
			mutate: func(spec *domain.JobSpec) {
				spec.Managed.Checkpoint.KeepLatest = 1001
			},
			wantError: "keepLatest",
		},
		{
			name: "best checkpoint retention overflow",
			mutate: func(spec *domain.JobSpec) {
				spec.Managed.Checkpoint.KeepBest = 1001
			},
			wantError: "keepBest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client, err := NewClient(ClientOptions{
				ServerURL: "https://platform.invalid",
				Token:     "test-token",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					return nil, errors.New("unexpected request")
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			spec := testJobSpec()
			spec.Source = domain.CodeSource{Type: "workspace-archive", ArtifactID: "artifact-ready"}
			spec.TrainingEngine = domain.TrainingEngineRayTrain
			spec.Managed.MaxFailures = 2
			test.mutate(&spec)
			original := spec

			_, err = client.Submit(context.Background(), spec)
			if requests != 0 {
				t.Fatalf("invalid managed submit made %d HTTP requests", requests)
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected local validation error containing %q, got %v", test.wantError, err)
			}
			if !reflect.DeepEqual(spec, original) {
				t.Fatalf("submit mutated the caller's spec: before=%+v after=%+v", original, spec)
			}
		})
	}
}

func TestSubmitRejectsInvalidFinalSpecBeforeCreateAPI(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.JobSpec)
		wantError string
	}{
		{
			name: "source",
			mutate: func(spec *domain.JobSpec) {
				spec.Source = domain.CodeSource{}
			},
			wantError: `unsupported source type ""`,
		},
		{
			name: "timeout",
			mutate: func(spec *domain.JobSpec) {
				spec.TimeoutSeconds = -1
			},
			wantError: "timeoutSeconds must not be negative",
		},
		{
			name: "retry",
			mutate: func(spec *domain.JobSpec) {
				spec.RetryPolicy.MaxRetries = 4
			},
			wantError: "maxRetries must be between 0 and 3",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client, err := NewClient(ClientOptions{
				ServerURL: "https://platform.invalid",
				Token:     "test-token",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					return nil, errors.New("unexpected request")
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			spec := testJobSpec()
			spec.Source = domain.CodeSource{Type: "workspace-archive", ArtifactID: "artifact-ready"}
			test.mutate(&spec)

			_, err = client.Submit(context.Background(), spec)
			if requests != 0 {
				t.Fatal("invalid final job spec must fail before the create API")
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected validation error containing %q, got %v", test.wantError, err)
			}
		})
	}
}

func TestPlatformLimitsDecodesAuthenticatedCachePolicy(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/limits" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("API token missing")
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"cache": map[string]any{
			"enabled": true, "modes": []string{"off", "runtime"}, "allowedSizes": []string{"100Gi", "200Gi"},
			"defaultSize": "200Gi", "maxSize": "500Gi",
		}, "runtime": map[string]any{
			"availableEngines": []string{"ray-ddp", "ray-train"}, "managedEnabled": true,
			"productionRayVersion": "2.56.1", "canaryRayVersion": "2.58.0",
		}})
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	limits, err := client.PlatformLimits(context.Background())
	if err != nil {
		t.Fatalf("read platform limits: %v", err)
	}
	if !limits.Cache.Enabled || limits.Cache.DefaultSize != "200Gi" || limits.Cache.MaxSize != "500Gi" || !reflect.DeepEqual(limits.Cache.Modes, []string{"off", "runtime"}) || !reflect.DeepEqual(limits.Cache.AllowedSizes, []string{"100Gi", "200Gi"}) {
		t.Fatalf("unexpected limits: %+v", limits)
	}
	if !limits.Runtime.ManagedEnabled || !reflect.DeepEqual(limits.Runtime.AvailableEngines, []string{"ray-ddp", "ray-train"}) || limits.Runtime.ProductionRayVersion != "2.56.1" {
		t.Fatalf("unexpected runtime capabilities: %+v", limits.Runtime)
	}
}

func TestPlatformLimitsRejectsInconsistentManagedCapabilities(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"runtime": map[string]any{
			"availableEngines": []string{"ray-train"}, "managedEnabled": true,
		}})
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PlatformLimits(context.Background()); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("inconsistent capabilities must fail closed, got %v", err)
	}
}

func TestTrainingImagesDecodesAndCopiesRuntimeCompatibility(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
			"name": "Managed", "reference": "registry/managed:2.56.1", "framework": "pytorch", "isDefault": true,
			"rayVersion": domain.RayVersionProduction, "supportedEngines": []string{"ray-ddp", "ray-train"},
		}})
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	images, err := client.TrainingImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].RayVersion != domain.RayVersionProduction || !reflect.DeepEqual(images[0].SupportedEngines, []domain.TrainingEngine{domain.TrainingEngineRayDDP, domain.TrainingEngineRayTrain}) {
		t.Fatalf("runtime compatibility was not decoded: %+v", images)
	}
	copy := cloneCatalogImage(images[0])
	images[0].SupportedEngines[0] = domain.TrainingEngineRayTrain
	if copy.SupportedEngines[0] != domain.TrainingEngineRayDDP {
		t.Fatalf("catalog engine slices alias caller-owned memory: %+v", copy)
	}
}

func TestTrainingImagesRejectsMalformedRuntimeCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		version any
		engines any
	}{
		{name: "missing version", version: "", engines: []string{"ray-ddp"}},
		{name: "unknown version", version: "2.99.0", engines: []string{"ray-ddp"}},
		{name: "empty engines", version: domain.RayVersionProduction, engines: []string{}},
		{name: "unknown engine", version: domain.RayVersionProduction, engines: []string{"ray-magic"}},
		{name: "duplicate engine", version: domain.RayVersionProduction, engines: []string{"ray-ddp", "ray-ddp"}},
		{name: "legacy managed", version: domain.RayVersionLegacy, engines: []string{"ray-train"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
					"name": "Broken", "reference": "registry/broken:tag", "rayVersion": test.version, "supportedEngines": test.engines,
				}})
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.TrainingImages(context.Background()); err == nil || !strings.Contains(err.Error(), "image catalogue") {
				t.Fatalf("malformed image metadata was accepted: %v", err)
			}
		})
	}
}

func TestDebugOutputRedactsTokenAndPresignedSignature(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("X-Tos-Signature") != "secret-signature" {
			t.Fatalf("signature was not sent to the direct upload endpoint")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	archivePath := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(archivePath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	var debug strings.Builder
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret-token", HTTPClient: server.Client(), DebugWriter: &debug})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), Artifact{UploadURL: server.URL + "/upload?X-Tos-Signature=secret-signature", ContentLength: 3}, Archive{Path: archivePath, SizeBytes: 3}); err != nil {
		t.Fatal(err)
	}
	output := debug.String()
	if strings.Contains(output, "secret-token") || strings.Contains(output, "X-Tos-Signature=secret") || !strings.Contains(output, "X-Tos-Signature=REDACTED") {
		t.Fatalf("debug leaked credential: %q", output)
	}
}

func TestLoadTokenRejectsGroupReadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"token":"stored-token"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken("", path); err == nil {
		t.Fatal("group-readable token config was accepted")
	}
}

func TestStatusEscapesUserProvidedJobIDAsOnePathSegment(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.EscapedPath() != "/api/v1/jobs/job%2Fother" {
			t.Fatalf("escaped path=%q", request.URL.EscapedPath())
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"id": "job/other"})
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background(), "job/other"); err != nil {
		t.Fatal(err)
	}
}

func TestNewClientRejectsInsecurePlatformURLBeforeRequest(t *testing.T) {
	if _, err := NewClient(ClientOptions{ServerURL: "http://platform.example.internal", Token: "test-token"}); err == nil {
		t.Fatal("HTTP platform URL was accepted")
	}
}

func TestUploadTransportFailureNeverLeaksPresignedQuery(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(archivePath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ServerURL: "https://platform.example.internal", Token: "test-token",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("request failed for " + request.URL.String())
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Upload(context.Background(), Artifact{UploadURL: "https://upload.example.internal/package?X-Tos-Signature=secret-signature", ContentLength: 3}, Archive{Path: archivePath, SizeBytes: 3})
	if err == nil {
		t.Fatal("upload transport failure was accepted")
	}
	if strings.Contains(err.Error(), "secret-signature") || strings.Contains(err.Error(), "X-Tos-Signature") {
		t.Fatalf("upload error leaked signed URL: %q", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestLogsAndCancelUseEstablishedJobEndpoints(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.EscapedPath()+"?"+request.URL.RawQuery)
		switch request.URL.Path {
		case "/api/v1/jobs/job-test/logs":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{}})
		case "/api/v1/jobs/job-test/cancel":
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected path=%s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Logs(context.Background(), "job-test", 25); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Cancel(context.Background(), "job-test"); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /api/v1/jobs/job-test/logs?limit=25", "POST /api/v1/jobs/job-test/cancel?"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%v want=%v", requests, want)
	}
}

func TestLogsPageEncodesDirectionLimitAndCursor(t *testing.T) {
	var rawQuery string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rawQuery = request.URL.RawQuery
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{
			"jobId": "job-test", "items": []any{},
			"page": map[string]any{"direction": "forward", "limit": 2000, "hasMore": false, "nextCursor": ""},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.LogsPage(context.Background(), "job-test", LogPageOptions{
		Limit: 2000, Direction: "forward", Cursor: "2026-08-22T16:00:00.123456789Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("limit") != "2000" || values.Get("direction") != "forward" || values.Get("after") != "2026-08-22T16:00:00.123456789Z" {
		t.Fatalf("unexpected log page query: %s", rawQuery)
	}
	if page.JobID != "job-test" || page.Page.Direction != "forward" {
		t.Fatalf("unexpected decoded page: %+v", page)
	}
	if !page.PaginationAvailable {
		t.Fatal("pagination metadata was present but not detected")
	}
}

// Training logs can easily exceed one MiB because model summaries are printed
// before the first loss line. The CLI must not truncate a valid JSON response
// and turn it into the misleading "invalid platform response" error.
func TestLogsAcceptsResponseLargerThanGenericAPILimit(t *testing.T) {
	largeLine := strings.Repeat("model-layer ", 120000) + "final-loss-marker"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/jobs/job-large/logs" {
			t.Fatalf("unexpected path=%s", request.URL.Path)
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{
			"jobId": "job-large",
			"items": []map[string]any{{"timestamp": "2026-08-21T00:00:00Z", "line": largeLine}},
		})
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	logs, err := client.Logs(context.Background(), "job-large", 3000)
	if err != nil {
		t.Fatalf("read large logs: %v", err)
	}
	if !strings.Contains(string(logs), "final-loss-marker") {
		t.Fatal("large log response was truncated")
	}
}

func testJobSpec() domain.JobSpec {
	return domain.JobSpec{
		Name: "spk-rayjob-test", Image: "registry.example/ray@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"}, Queue: "tenant-a-gpu",
	}
}

func serverURL(request *http.Request) string {
	if request.TLS != nil {
		return "https://" + request.Host
	}
	return "http://" + request.Host
}

func writeTestCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test TLS certificate is unavailable")
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeClientSuccess(t *testing.T, writer http.ResponseWriter, status int, data any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(map[string]any{"success": true, "data": data, "request_id": "test"}); err != nil {
		t.Fatal(err)
	}
}

func writeClientFailure(t *testing.T, writer http.ResponseWriter, status int, code string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(map[string]any{"success": false, "error": map[string]string{"code": code, "message": "request rejected"}, "request_id": "test"}); err != nil {
		t.Fatal(err)
	}
}
