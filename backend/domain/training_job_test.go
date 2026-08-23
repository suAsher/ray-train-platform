package domain

import (
	"strings"
	"testing"
)

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
