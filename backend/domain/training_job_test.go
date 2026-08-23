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
		{name: "invalid mode", cache: CacheRequest{Mode: "always"}, wantErr: "unsupported cache mode"},
		{name: "omitted with size", cache: CacheRequest{Size: "200Gi"}, wantErr: "off cache cannot specify size"},
		{name: "off with size", cache: CacheRequest{Mode: CacheModeOff, Size: "200Gi"}, wantErr: "off cache cannot specify size"},
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

	payload, err := json.Marshal(JobSpec{Cache: CacheRequest{Mode: CacheModeRuntime, Size: "200Gi"}})
	if err != nil {
		t.Fatalf("encode job spec: %v", err)
	}
	if !strings.Contains(string(payload), `"cache":{"mode":"runtime","size":"200Gi"}`) {
		t.Fatalf("unexpected cache JSON shape: %s", payload)
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
