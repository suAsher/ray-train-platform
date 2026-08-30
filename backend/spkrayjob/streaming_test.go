package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestParseDataModeAcceptsStreamingAndListsItOnError(t *testing.T) {
	mode, err := parseDataMode(" streaming ", projectCache{})
	if err != nil || mode != domain.DataModeStreaming {
		t.Fatalf("parse streaming mode: mode=%q err=%v", mode, err)
	}

	_, err = parseDataMode("remote-files", projectCache{})
	if err == nil || !strings.Contains(err.Error(), string(domain.DataModeStreaming)) {
		t.Fatalf("unsupported-mode error must list streaming, got %v", err)
	}
	if !strings.Contains(helpText, "--data-mode streaming") || !strings.Contains(helpText, "--dataset-cache-policy bounded") {
		t.Fatalf("top-level help does not explain streaming submission:\n%s", helpText)
	}
}

func TestProjectFileLoadsStreamingDatasetReferenceAndBoundedCachePolicy(t *testing.T) {
	root := t.TempDir()
	contents := `name: s1h-streaming
entrypoint: python tools/westwell_train.py configs/s1h.yaml
engine: ray-train
dataMode: streaming
datasetRef:
  dataset: labeled-full
  version: dataset-version-20260830
cachePolicy: bounded
workers: 2
gpusPerWorker: 8
`
	if err := os.WriteFile(filepath.Join(root, projectFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadProject(root)
	if err != nil {
		t.Fatalf("load streaming project: %v", err)
	}
	if loaded.DatasetRef != (domain.DatasetReference{Dataset: "labeled-full", Version: "dataset-version-20260830"}) {
		t.Fatalf("datasetRef was not loaded exactly: %+v", loaded.DatasetRef)
	}
	if loaded.CachePolicy != domain.DatasetCachePolicyBounded {
		t.Fatalf("cachePolicy=%q", loaded.CachePolicy)
	}
}

func TestProjectFileRejectsInternalDatasetManifestFields(t *testing.T) {
	root := t.TempDir()
	contents := `dataMode: streaming
datasetRef:
  dataset: labeled-full
  version: latest
manifestKey: ray-train/platform/datasets/private/manifest.parquet
cachePolicy: bounded
`
	if err := os.WriteFile(filepath.Join(root, projectFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProject(root); err == nil || !strings.Contains(err.Error(), "manifestKey") {
		t.Fatalf("internal manifest field must be rejected by strict project decoding, got %v", err)
	}
}

func TestStreamingProjectBuildsOnlyPublicDatasetReference(t *testing.T) {
	value := project{
		Name: "s1h-streaming", Image: "registry.example/streaming@sha256:" + strings.Repeat("a", 64),
		Entrypoint: "python tools/westwell_train.py configs/s1h.yaml", Engine: "ray-train",
		DataMode: "streaming", Workers: 2, GPUsPerWorker: 8,
		DatasetRef:  domain.DatasetReference{Dataset: "labeled-full", Version: "dataset-version-20260830"},
		CachePolicy: domain.DatasetCachePolicyBounded,
	}

	spec, err := value.jobSpec()
	if err != nil {
		t.Fatalf("build streaming spec: %v", err)
	}
	if spec.TrainingEngine != domain.TrainingEngineRayTrain || spec.DataMode != domain.DataModeStreaming {
		t.Fatalf("streaming runtime selection was lost: %+v", spec)
	}
	if spec.DatasetRef != value.DatasetRef || spec.CachePolicy != domain.DatasetCachePolicyBounded {
		t.Fatalf("public dataset selection was lost: ref=%+v policy=%q", spec.DatasetRef, spec.CachePolicy)
	}
	if spec.DatasetURI != "" || spec.DatasetStorage != (domain.StorageSelection{}) ||
		spec.Input != (domain.DataLocation{}) || spec.ResolvedDataMounts != (domain.ResolvedDataSpaceMounts{}) ||
		spec.ResolvedDataRoots != (domain.ResolvedDataSpaceRoots{}) || !spec.Managed.RayData.IsZero() ||
		spec.Cache != (domain.CacheRequest{}) {
		t.Fatalf("streaming spec exposed legacy or resolved storage details: %+v", spec)
	}
	if err := validatePreflightJobSpec(spec); err != nil {
		t.Fatalf("streaming spec must satisfy the shared domain contract locally: %v", err)
	}
}

func TestStreamingProjectRequiresRayTrainAndCompleteDatasetReference(t *testing.T) {
	base := project{
		Name: "s1h-streaming", Image: "registry.example/streaming@sha256:" + strings.Repeat("b", 64),
		Entrypoint: "python train.py", DataMode: "streaming", Workers: 1, GPUsPerWorker: 1,
		DatasetRef: domain.DatasetReference{Dataset: "labeled-full", Version: "latest"}, CachePolicy: domain.DatasetCachePolicyAuto,
	}

	base.Engine = "ray-ddp"
	if _, err := base.jobSpec(); err == nil || !strings.Contains(err.Error(), "ray-train") {
		t.Fatalf("streaming with ray-ddp must fail clearly, got %v", err)
	}

	base.Engine = "ray-train"
	base.DatasetRef.Version = ""
	if _, err := base.jobSpec(); err == nil || !strings.Contains(err.Error(), "datasetRef") {
		t.Fatalf("partial datasetRef must fail locally, got %v", err)
	}

	base.DatasetRef.Version = "latest"
	base.CachePolicy = domain.DatasetCachePolicy("unbounded")
	if _, err := base.jobSpec(); err == nil || !strings.Contains(err.Error(), "cache policy") {
		t.Fatalf("unknown cache policy must fail locally, got %v", err)
	}

	base.CachePolicy = ""
	spec, err := base.jobSpec()
	if err != nil || spec.CachePolicy != domain.DatasetCachePolicyAuto {
		t.Fatalf("omitted streaming cache policy must resolve to auto: spec=%+v err=%v", spec, err)
	}
}

func TestStreamingImageSelectionRequiresEnabledCanaryRuntime(t *testing.T) {
	production := catalogImage{
		Reference: "registry.example/production:2.56", Name: "Production", IsDefault: true,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	canary := catalogImage{
		Reference: "registry.example/canary:2.58", Name: "Canary",
		RayVersion: domain.RayVersionCanary, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	runtime := PlatformRuntimeLimits{
		AvailableEngines: []string{"ray-ddp", "ray-train"}, ManagedEnabled: true,
		ProductionRayVersion: domain.RayVersionProduction, CanaryRayVersion: domain.RayVersionCanary,
	}
	if _, err := managedImageForDataMode([]catalogImage{production, canary}, "", runtime, domain.DataModeStreaming); err == nil || !strings.Contains(err.Error(), "canary") {
		t.Fatalf("disabled canary runtime must reject streaming, got %v", err)
	}
	runtime.CanaryEnabled = true
	selected, err := managedImageForDataMode([]catalogImage{production, canary}, "", runtime, domain.DataModeStreaming)
	if err != nil || selected.Reference != canary.Reference {
		t.Fatalf("streaming must ignore the production default and choose canary: %+v err=%v", selected, err)
	}
	if _, err := managedImageForDataMode([]catalogImage{production, canary}, production.Reference, runtime, domain.DataModeStreaming); err == nil || !strings.Contains(err.Error(), domain.RayVersionCanary) {
		t.Fatalf("explicit production image must be rejected for streaming, got %v", err)
	}
	if _, err := managedImageForDataMode([]catalogImage{production}, "", runtime, domain.DataModeStreaming); err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("missing canary image must fail clearly, got %v", err)
	}
}

func TestClientPreflightManagedStreamingSelectsCanaryImage(t *testing.T) {
	canaryImage := "registry.example/canary@sha256:" + strings.Repeat("d", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/limits":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"runtime": map[string]any{
				"availableEngines": []string{"ray-ddp", "ray-train"}, "managedEnabled": true, "canaryEnabled": true,
				"productionRayVersion": domain.RayVersionProduction, "canaryRayVersion": domain.RayVersionCanary,
			}})
		case "/api/v1/images":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "Streaming", "reference": canaryImage, "rayVersion": domain.RayVersionCanary,
				"supportedEngines": []string{"ray-train"},
			}})
		default:
			t.Fatalf("unexpected preflight request %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	spec := domain.JobSpec{TrainingEngine: domain.TrainingEngineRayTrain, DataMode: domain.DataModeStreaming}
	resolved, err := client.preflightManagedImage(context.Background(), spec)
	if err != nil || resolved.Image != canaryImage {
		t.Fatalf("managed streaming preflight did not bind canary image: %+v err=%v", resolved, err)
	}

	legacy := domain.JobSpec{TrainingEngine: domain.TrainingEngineRayDDP, Image: "legacy-image"}
	resolved, err = client.preflightManagedImage(context.Background(), legacy)
	if err != nil || resolved.Image != legacy.Image {
		t.Fatalf("legacy preflight behavior changed: %+v err=%v", resolved, err)
	}
}

func TestInitStreamingRequiresRayTrainAndWritesExplicitMode(t *testing.T) {
	root := t.TempDir()
	err := Run(context.Background(), []string{"init", "--dir", root, "--data-mode", "streaming"}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err == nil || !strings.Contains(err.Error(), "ray-train") {
		t.Fatalf("streaming init with default ray-ddp must fail, got %v", err)
	}
	err = Run(context.Background(), []string{
		"init", "--dir", root, "--engine", "ray-train", "--data-mode", "streaming",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("initialize streaming project: %v", err)
	}
	loaded, err := loadProject(root)
	if err != nil || loaded.DataMode != string(domain.DataModeStreaming) || loaded.Engine != string(domain.TrainingEngineRayTrain) {
		t.Fatalf("streaming starter is unusable: %+v err=%v", loaded, err)
	}
}

func TestStreamingSubmitFlagsOverrideProjectWithoutInternalManifestDetails(t *testing.T) {
	root := seedProject(t, `name: s1h-streaming
entrypoint: python train.py
engine: ray-train
dataMode: streaming
datasetRef:
  dataset: old-dataset
  version: old-version
cachePolicy: off
workers: 2
gpusPerWorker: 8
`)

	canaryImage := "harbor.example/streaming@sha256:" + strings.Repeat("c", 64)
	var submitted domain.JobSpec
	var submittedBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/limits":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"runtime": map[string]any{
				"availableEngines": []string{"ray-ddp", "ray-train"}, "managedEnabled": true, "canaryEnabled": true,
				"productionRayVersion": domain.RayVersionProduction, "canaryRayVersion": domain.RayVersionCanary,
			}})
		case "/api/v1/images":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "Streaming", "reference": canaryImage, "rayVersion": domain.RayVersionCanary,
				"supportedEngines": []string{"ray-train"},
			}})
		case "/api/v1/jobs/preflight":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"image": canaryImage, "trainingEngine": "ray-train", "rayVersion": domain.RayVersionCanary, "requestedGpus": 16,
				"dataset": map[string]any{
					"datasetId": "labeled-full", "datasetSlug": "labeled-full", "versionId": "dataset-version-20260830",
					"version": "2026.08.30", "manifestSha256": strings.Repeat("a", 64), "schemaVersion": "s1h-v1",
					"trainSamples": 15228, "valSamples": 1620, "testSamples": 0,
					"logicalBytes": int64(10) << 40, "packedBytes": int64(8) << 40,
					"dataMode": "streaming", "cachePolicy": "bounded",
				},
			})
		case "/api/v1/source-artifacts":
			var create struct {
				SHA256    string `json:"sha256"`
				SizeBytes int64  `json:"sizeBytes"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": "artifact-streaming", "state": "PENDING", "sha256": create.SHA256,
				"sizeBytes": create.SizeBytes, "uploadUrl": serverURL(request) + "/upload",
				"contentLength": create.SizeBytes, "uploadRequired": true,
			})
		case "/upload":
			writer.WriteHeader(http.StatusOK)
		case "/api/v1/source-artifacts/artifact-streaming/complete":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": "artifact-streaming", "state": "READY"})
		case "/api/v1/jobs":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			submittedBody = append([]byte(nil), body...)
			var envelope struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatal(err)
			}
			submitted = envelope.Spec
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-streaming"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--dataset", "labeled-full:dataset-version-20260830", "--dataset-cache-policy", "bounded",
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit streaming project: %v", err)
	}
	if submitted.DatasetRef != (domain.DatasetReference{Dataset: "labeled-full", Version: "dataset-version-20260830"}) ||
		submitted.CachePolicy != domain.DatasetCachePolicyBounded || submitted.DataMode != domain.DataModeStreaming {
		t.Fatalf("submitted public selection is wrong: %+v", submitted)
	}
	if submitted.Image != canaryImage {
		t.Fatalf("streaming must select the enabled Ray canary image, got %q", submitted.Image)
	}
	for _, forbidden := range [][]byte{
		[]byte("manifestObjectKey"), []byte("manifestKey"), []byte(domain.DefaultDatasetInternalPrefix),
		[]byte(`"datasetUri"`), []byte("tos://"),
	} {
		if bytes.Contains(submittedBody, forbidden) {
			t.Fatalf("job request exposed internal or resolved storage field %q: %s", forbidden, submittedBody)
		}
	}
}

func TestStreamingSubmitSupportsSeparateDatasetVersionFlag(t *testing.T) {
	override, err := parseDatasetFlag("labeled-full", "dataset-version-20260830", true, true)
	if err != nil {
		t.Fatalf("parse explicit dataset/version flags: %v", err)
	}
	if override.Reference != (domain.DatasetReference{Dataset: "labeled-full", Version: "dataset-version-20260830"}) ||
		!override.DatasetProvided || !override.VersionProvided {
		t.Fatalf("unexpected dataset flag override: %+v", override)
	}

	if _, err := parseDatasetFlag("labeled-full:latest", "dataset-version-20260830", true, true); err == nil {
		t.Fatal("embedded and separate dataset versions must not be accepted together")
	}
	for _, raw := range []string{"tos://private/key", "labeled-full:", "dataset:version:extra"} {
		if _, err := parseDatasetFlag(raw, "", true, false); err == nil {
			t.Fatalf("unsafe or incomplete dataset shorthand %q was accepted", raw)
		}
	}
}

func TestStreamingDatasetFlagsOverrideProjectFieldsIndependently(t *testing.T) {
	base := project{
		DatasetRef:  domain.DatasetReference{Dataset: "old-dataset", Version: "old-version"},
		CachePolicy: domain.DatasetCachePolicyOff,
	}
	override, err := parseDatasetFlag("labeled-full", "dataset-version-20260830", true, true)
	if err != nil {
		t.Fatal(err)
	}
	merged := base.merge(submitOverrides{
		DatasetRef: override.Reference, CachePolicy: domain.DatasetCachePolicyBounded,
		providedDataset: override.DatasetProvided, providedDatasetVersion: override.VersionProvided, providedCachePolicy: true,
	})
	if merged.DatasetRef != override.Reference || merged.CachePolicy != domain.DatasetCachePolicyBounded {
		t.Fatalf("explicit streaming flags did not override project defaults: %+v", merged)
	}

	versionOnly, err := parseDatasetFlag("", "new-version", false, true)
	if err != nil {
		t.Fatal(err)
	}
	merged = base.merge(submitOverrides{
		DatasetRef: versionOnly.Reference, providedDataset: versionOnly.DatasetProvided, providedDatasetVersion: versionOnly.VersionProvided,
	})
	if merged.DatasetRef != (domain.DatasetReference{Dataset: "old-dataset", Version: "new-version"}) {
		t.Fatalf("version-only override must preserve the committed dataset: %+v", merged.DatasetRef)
	}
}

func TestStreamingRejectsLegacyCacheFlagsBeforeClientConfiguration(t *testing.T) {
	root := seedProject(t, `name: s1h-streaming
entrypoint: python train.py
engine: ray-train
dataMode: streaming
datasetRef:
  dataset: labeled-full
  version: latest
cachePolicy: bounded
workers: 1
gpusPerWorker: 1
`)
	err := Run(context.Background(), []string{
		"submit", "--dir", root, "--cache-mode", "runtime", "--cache-size", "1Ti",
	}, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "--dataset-cache-policy") {
		t.Fatalf("legacy cache flags must fail before client configuration, got %v", err)
	}
}
