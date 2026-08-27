package domain

import (
	"strings"
	"testing"
)

func managedEntrypointSpec(entrypoint Entrypoint) JobSpec {
	return JobSpec{
		Name:           "managed-entrypoint",
		Image:          "registry.example.com/ray@sha256:" + strings.Repeat("a", 64),
		Source:         CodeSource{Type: "git", URL: "https://git.example.com/team/train.git", Commit: "0123456789abcdef"},
		Entrypoint:     entrypoint,
		TrainingEngine: TrainingEngineRayTrain,
		RayVersion:     RayVersionProduction,
		Managed: ManagedTrainingPolicy{
			MaxFailures: 2,
			Checkpoint:  CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
		},
		Resources: Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"},
		Queue:     "tenant-gpu",
	}
}

func TestManagedEntrypointValidationAcceptsOnlySupportedPythonForms(t *testing.T) {
	tests := []Entrypoint{
		{Command: []string{"python", "tools/train.py"}, Args: []string{"--epochs", "2"}},
		{Command: []string{"python", "-m", "package.train"}, Args: []string{"--epochs", "2"}},
		{Command: []string{"/bin/sh", "-lc", "python tools/train.py --name 'managed run'"}},
		{Command: []string{"/bin/sh", "-lc", "python -m package.train --epochs 2"}},
	}
	for _, entrypoint := range tests {
		spec := managedEntrypointSpec(entrypoint)
		if err := spec.Validate(); err != nil {
			t.Fatalf("supported managed entrypoint %#v was rejected: %v", entrypoint, err)
		}
	}
}

func TestManagedEntrypointValidationRejectsUnsafeForms(t *testing.T) {
	tests := []Entrypoint{
		{Command: []string{"torchrun", "train.py"}},
		{Command: []string{"bash", "train.sh"}},
		{Command: []string{"python3", "train.py"}},
		{Command: []string{"python", "-c", "print('unsafe')"}},
		{Command: []string{"python", "/tmp/train.py"}},
		{Command: []string{"python", "../train.py"}},
		{Command: []string{"python", `tools\train.py`}},
		{Command: []string{"python", "-m", "package/train"}},
		{Command: []string{"python", "-m", "package..train"}},
		{Command: []string{"/bin/sh", "-lc", "torchrun train.py"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py && echo pwned"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py; echo pwned"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py | tee output"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py > output"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py\npython second.py"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py $(id)"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py `id`"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py --name 'unfinished"}},
		{Command: []string{"/bin/sh", "-c", "python train.py"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py"}, Args: []string{"--extra"}},
	}
	for _, entrypoint := range tests {
		spec := managedEntrypointSpec(entrypoint)
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "managed entrypoint") {
			t.Fatalf("unsafe managed entrypoint %#v was accepted or returned unclear error: %v", entrypoint, err)
		}
	}
}

func TestLegacyRayDDPEntrypointBehaviorIsUnchanged(t *testing.T) {
	spec := managedEntrypointSpec(Entrypoint{Command: []string{"bash", "legacy-train.sh"}})
	spec.TrainingEngine = TrainingEngineRayDDP
	spec.RayVersion = RayVersionLegacy
	spec.Managed = ManagedTrainingPolicy{}

	if err := spec.Validate(); err != nil {
		t.Fatalf("legacy ray-ddp entrypoint must retain existing behavior: %v", err)
	}
}
