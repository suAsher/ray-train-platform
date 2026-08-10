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
