package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCacheRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		cache   CacheRequest
		wantErr string
	}{
		{name: "omitted is backward compatible", cache: CacheRequest{}},
		{name: "off", cache: CacheRequest{Mode: CacheModeOff}},
		{name: "runtime", cache: CacheRequest{Mode: CacheModeRuntime, Size: "200Gi"}},
		{name: "runtime with automatic input preload", cache: CacheRequest{Mode: CacheModeRuntime, Size: "200Gi", Preload: CachePreloadInput}},
		{name: "invalid mode", cache: CacheRequest{Mode: "always"}, wantErr: "unsupported cache mode"},
		{name: "invalid preload", cache: CacheRequest{Mode: CacheModeRuntime, Size: "200Gi", Preload: "dataset"}, wantErr: "unsupported cache preload"},
		{name: "omitted with size", cache: CacheRequest{Size: "200Gi"}, wantErr: "off cache cannot specify size"},
		{name: "off with size", cache: CacheRequest{Mode: CacheModeOff, Size: "200Gi"}, wantErr: "off cache cannot specify size"},
		{name: "off with preload", cache: CacheRequest{Mode: CacheModeOff, Preload: CachePreloadInput}, wantErr: "off cache cannot specify preload"},
		{name: "runtime without size", cache: CacheRequest{Mode: CacheModeRuntime}, wantErr: "runtime cache size is required"},
		{name: "runtime invalid size", cache: CacheRequest{Mode: CacheModeRuntime, Size: "large"}, wantErr: "positive Kubernetes storage quantity"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.cache.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("validate cache: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestJobSpecCacheJSONShapeAndOmittedCompatibility(t *testing.T) {
	var legacy JobSpec
	if err := json.Unmarshal([]byte(`{"name":"legacy"}`), &legacy); err != nil {
		t.Fatalf("decode legacy job spec: %v", err)
	}
	if legacy.Cache.Mode != "" || legacy.Cache.Size != "" {
		t.Fatalf("legacy cache must remain omitted: %#v", legacy.Cache)
	}

	payload, err := json.Marshal(JobSpec{Cache: CacheRequest{Mode: CacheModeRuntime, Size: "200Gi", Preload: CachePreloadInput}})
	if err != nil {
		t.Fatalf("encode job spec: %v", err)
	}
	if !strings.Contains(string(payload), `"cache":{"mode":"runtime","size":"200Gi","preload":"input"}`) {
		t.Fatalf("unexpected cache JSON shape: %s", payload)
	}
}

func TestJobSpecValidateRequiresExactGovernedInputForAutomaticPreload(t *testing.T) {
	base := JobSpec{
		Name:       "cache-preload",
		Image:      "registry.example.com/ray-train@sha256:" + strings.Repeat("a", 64),
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 2, GPUsPerWorker: 8, CPUPerWorker: 32, MemoryPerWorker: "128Gi"},
		Queue:      "team-gpu-queue",
		Cache:      CacheRequest{Mode: CacheModeRuntime, Size: "1Ti", Preload: CachePreloadInput},
	}
	for _, test := range []struct {
		name    string
		input   DataLocation
		wantErr string
	}{
		{name: "missing input", wantErr: "automatic cache preload requires"},
		{name: "whole logical root", input: DataLocation{Space: DataSpacePublic}, wantErr: "non-empty input path"},
		{name: "exact public dataset", input: DataLocation{Space: DataSpacePublic, RelativePath: "labeled/fz-v1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			spec.Input = test.input
			err := spec.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("validate preloaded job: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("expected %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestJobSpecJSONOmitsZeroValueCache(t *testing.T) {
	payload, err := json.Marshal(JobSpec{})
	if err != nil {
		t.Fatalf("encode zero-value job spec: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode zero-value job spec: %v", err)
	}
	if _, exists := fields["cache"]; exists {
		t.Fatalf("zero-value cache must be omitted: %s", payload)
	}
}

func TestJobSpecValidateAcceptsPinnedTrainingJob(t *testing.T) {
	spec := JobSpec{
		Name:       "llama-sft-run-001",
		Image:      "registry.example.com/ray-train@sha256:" + strings.Repeat("a", 64),
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 3, GPUsPerWorker: 8, CPUPerWorker: 32, MemoryPerWorker: "128Gi"},
		Queue:      "llm-gpu-queue",
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("expected valid spec: %v", err)
	}
}

func TestJobSpecValidateAcceptsCataloguedTaggedTrainingImage(t *testing.T) {
	spec := JobSpec{
		Name:       "bevfusion-tagged-run",
		Image:      "harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion:stable-cuda121",
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"},
		Queue:      "team-gpu-queue",
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("an explicitly tagged catalogue image should be valid: %v", err)
	}
}

func TestJobSpecValidateRejectsImageWithoutTagOrDigest(t *testing.T) {
	spec := JobSpec{
		Name:       "bevfusion-untagged-run",
		Image:      "harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion",
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"},
		Queue:      "team-gpu-queue",
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("an image without an explicit tag or digest must be rejected")
	}
}

func TestValidateRuntimeImageRejectsMalformedTags(t *testing.T) {
	invalid := []string{
		"registry.example.com/team/runtime:stable:extra",
		"registry.example.com/team/runtime:",
		"registry.example.com//team/runtime:stable",
		"registry.example.com/team/runtime:bad/tag",
	}
	for _, image := range invalid {
		if err := ValidateRuntimeImage(image); err == nil {
			t.Errorf("expected malformed image %q to be rejected", image)
		}
	}
}

func TestJobSpecValidateRejectsUnpinnedImageAndSource(t *testing.T) {
	spec := JobSpec{
		Name:       "llama-sft-run-001",
		Image:      "registry.example.com/ray-train:latest",
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 1, GPUsPerWorker: 1},
		Queue:      "llm-gpu-queue",
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("expected image and source validation error")
	}
}

func TestJobSpecValidateRejectsResourceOverflow(t *testing.T) {
	spec := JobSpec{
		Name:       "llama-sft-run-001",
		Image:      "registry.example.com/ray-train@sha256:" + strings.Repeat("a", 64),
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 4, GPUsPerWorker: 9},
		Queue:      "llm-gpu-queue",
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("expected resource validation error")
	}
}

func TestJobSpecValidateRequiresArtifactIDForArtifactSource(t *testing.T) {
	spec := JobSpec{
		Name:       "artifact-train-001",
		Image:      "registry.example.com/ray-train@sha256:" + strings.Repeat("a", 64),
		Source:     CodeSource{Type: "artifact"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 1, GPUsPerWorker: 1},
		Queue:      "tenant-a-gpu",
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("artifact source without artifactId was accepted")
	}

	spec.Source.ArtifactID = "artifact-01"
	if err := spec.Validate(); err != nil {
		t.Fatalf("artifact source with artifactId should be valid before materialization: %v", err)
	}
}

func TestManagedEngineRejectsRay235(t *testing.T) {
	spec := validJobSpec()
	spec.TrainingEngine = TrainingEngineRayTrain
	spec.RayVersion = "2.35.0"
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "Ray 2.56.1") {
		t.Fatalf("expected managed-runtime version rejection, got %v", err)
	}
}

func TestManagedEngineAcceptsProductionAndCanaryVersions(t *testing.T) {
	for _, version := range []string{RayVersionProduction, RayVersionCanary} {
		t.Run(version, func(t *testing.T) {
			spec := validJobSpec()
			spec.TrainingEngine = TrainingEngineRayTrain
			spec.RayVersion = version
			spec.Managed = ManagedTrainingPolicy{
				MaxFailures: 2,
				Checkpoint:  CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("expected Ray %s to support managed training: %v", version, err)
			}
		})
	}
}

func TestManagedEngineValidatesPolicy(t *testing.T) {
	spec := validJobSpec()
	spec.TrainingEngine = TrainingEngineRayTrain
	spec.RayVersion = RayVersionProduction
	spec.Managed.MaxFailures = 11
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "maxFailures") {
		t.Fatalf("expected managed policy rejection, got %v", err)
	}
}

func TestJobSpecTrainingEngineValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		engine  TrainingEngine
		wantErr string
	}{
		{name: "omitted legacy engine"},
		{name: "whitespace legacy engine", engine: " \t"},
		{name: "explicit ray ddp", engine: TrainingEngineRayDDP},
		{name: "unknown engine", engine: "spark", wantErr: "unsupported training engine"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := validJobSpec()
			spec.TrainingEngine = test.engine
			err := spec.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("validate job spec: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestJobSpecDataModeValidation(t *testing.T) {
	rayData, err := NewRayDataDatasetConfig(RayDataFormatImages, "images/train")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		prepare func(*JobSpec)
		wantErr string
	}{
		{name: "omitted mode", prepare: func(*JobSpec) {}},
		{name: "mount mode", prepare: func(spec *JobSpec) { spec.DataMode = DataModeMount }},
		{name: "ray data with managed engine", prepare: func(spec *JobSpec) {
			spec.TrainingEngine = TrainingEngineRayTrain
			spec.RayVersion = RayVersionProduction
			spec.DataMode = DataModeRayData
			spec.Managed.RayData = rayData
		}},
		{name: "ray data without dataset config", prepare: func(spec *JobSpec) {
			spec.TrainingEngine = TrainingEngineRayTrain
			spec.RayVersion = RayVersionProduction
			spec.DataMode = DataModeRayData
		}, wantErr: "Ray Data dataset config is required"},
		{name: "mount with ray data config", prepare: func(spec *JobSpec) {
			spec.TrainingEngine = TrainingEngineRayTrain
			spec.RayVersion = RayVersionProduction
			spec.DataMode = DataModeMount
			spec.Managed.RayData = rayData
		}, wantErr: "requires ray-data mode"},
		{name: "cache with ray data config", prepare: func(spec *JobSpec) {
			spec.TrainingEngine = TrainingEngineRayTrain
			spec.RayVersion = RayVersionProduction
			spec.DataMode = DataModeCache
			spec.Managed.RayData = rayData
			spec.Cache = CacheRequest{Mode: CacheModeRuntime, Size: "200Gi", Preload: CachePreloadInput}
			spec.Input = DataLocation{Space: DataSpacePublic, RelativePath: "datasets/train"}
		}, wantErr: "requires ray-data mode"},
		{name: "ray data with legacy engine", prepare: func(spec *JobSpec) {
			spec.DataMode = DataModeRayData
		}, wantErr: "ray-data requires ray-train"},
		{name: "cache with runtime input preload", prepare: func(spec *JobSpec) {
			spec.DataMode = DataModeCache
			spec.Cache = CacheRequest{Mode: CacheModeRuntime, Size: "200Gi", Preload: CachePreloadInput}
			spec.Input = DataLocation{Space: DataSpacePublic, RelativePath: "datasets/train"}
		}},
		{name: "cache without runtime cache", prepare: func(spec *JobSpec) {
			spec.DataMode = DataModeCache
		}, wantErr: "cache data mode requires runtime cache with preload=input"},
		{name: "cache without input preload", prepare: func(spec *JobSpec) {
			spec.DataMode = DataModeCache
			spec.Cache = CacheRequest{Mode: CacheModeRuntime, Size: "200Gi"}
		}, wantErr: "cache data mode requires runtime cache with preload=input"},
		{name: "unknown mode", prepare: func(spec *JobSpec) {
			spec.DataMode = "stream"
		}, wantErr: "unsupported data mode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := validJobSpec()
			test.prepare(&spec)
			err := spec.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("validate job spec: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestJobSpecParentJobIDValidation(t *testing.T) {
	for _, test := range []struct {
		name        string
		parentJobID string
		wantErr     bool
	}{
		{name: "omitted"},
		{name: "generated job id", parentJobID: "job-0123456789abcdef01234567"},
		{name: "too short", parentJobID: "job-0123", wantErr: true},
		{name: "uppercase hexadecimal", parentJobID: "job-0123456789ABCDEF01234567", wantErr: true},
		{name: "surrounding whitespace", parentJobID: " job-0123456789abcdef01234567 ", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := validJobSpec()
			spec.ParentJobID = test.parentJobID
			err := spec.Validate()
			if !test.wantErr && err != nil {
				t.Fatalf("validate job spec: %v", err)
			}
			if test.wantErr && (err == nil || !strings.Contains(err.Error(), "parentJobId")) {
				t.Fatalf("expected parentJobId rejection, got %v", err)
			}
		})
	}
}

func TestRayDDPRejectsUnsupportedRayVersion(t *testing.T) {
	spec := validJobSpec()
	spec.TrainingEngine = TrainingEngineRayDDP
	spec.RayVersion = "bogus"
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported Ray version") {
		t.Fatalf("expected unsupported Ray version rejection, got %v", err)
	}
}

func TestRayDDPAcceptsPlatformRayVersions(t *testing.T) {
	for _, version := range []string{"", RayVersionLegacy, RayVersionProduction, RayVersionCanary} {
		t.Run(version, func(t *testing.T) {
			spec := validJobSpec()
			spec.TrainingEngine = TrainingEngineRayDDP
			spec.RayVersion = version
			if err := spec.Validate(); err != nil {
				t.Fatalf("expected ray-ddp to accept Ray version %q: %v", version, err)
			}
		})
	}
}

func TestRayDDPRejectsNonZeroManagedTrainingPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy ManagedTrainingPolicy
	}{
		{name: "invalid policy", policy: ManagedTrainingPolicy{MaxFailures: 11}},
		{name: "valid max failures", policy: ManagedTrainingPolicy{MaxFailures: 1}},
		{name: "valid checkpoint policy", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{EveryEpochs: 1, KeepLatest: 2, KeepBest: 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := validJobSpec()
			spec.TrainingEngine = TrainingEngineRayDDP
			spec.Managed = test.policy
			if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "managed policy requires ray-train") {
				t.Fatalf("expected ray-ddp managed policy rejection, got %v", err)
			}
		})
	}
}

func TestLegacyExecutionRayTrainDoesNotSelectManagedEngine(t *testing.T) {
	spec := validJobSpec()
	spec.Execution.Mode = ExecutionModeRayTrain
	if err := spec.Validate(); err != nil {
		t.Fatalf("expected legacy ray_train execution mode to remain valid: %v", err)
	}
	if got := spec.TrainingEngine.Resolved(); got != TrainingEngineRayDDP {
		t.Fatalf("legacy execution mode resolved training engine to %q", got)
	}
}

func validJobSpec() JobSpec {
	return JobSpec{
		Name:       "managed-train-001",
		Image:      "registry.example.com/ray-train@sha256:" + strings.Repeat("a", 64),
		Source:     CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: 2, GPUsPerWorker: 8, CPUPerWorker: 32, MemoryPerWorker: "128Gi"},
		Queue:      "team-gpu-queue",
	}
}
