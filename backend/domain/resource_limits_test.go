package domain

import (
	"strings"
	"testing"
)

func specWithResources(workers, gpusPerWorker int) JobSpec {
	spec := JobSpec{
		Name:       "sized-job",
		Image:      "registry.example/ray@sha256:" + repeatChar('0', 64),
		Source:     CodeSource{Type: "git", URL: "https://git.example/train", Commit: "0123456789abcdef"},
		Entrypoint: Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  Resources{WorkerReplicas: workers, GPUsPerWorker: gpusPerWorker, CPUPerWorker: 8, MemoryPerWorker: "32Gi"},
		Queue:      "team-a-gpu",
	}
	return spec
}

func repeatChar(c byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = c
	}
	return string(out)
}

func TestDefaultResourceLimitsMatchTheInitialCluster(t *testing.T) {
	SetResourceLimits(ResourceLimits{})
	if err := specWithResources(3, 8).Validate(); err != nil {
		t.Fatalf("3 x 8 = 24 GPUs must be allowed by default: %v", err)
	}
	if err := specWithResources(4, 8).Validate(); err == nil {
		t.Fatalf("expected the default worker ceiling to reject 4 workers")
	}
}

func TestJobSpecShapeDefersCapacityWithoutChangingServerLimits(t *testing.T) {
	SetResourceLimits(ResourceLimits{})
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	before := CurrentResourceLimits()
	for _, shape := range []struct{ workers, gpus int }{{4, 8}, {2, 16}, {999, 1}} {
		spec := specWithResources(shape.workers, shape.gpus)
		spec.Execution.Mode = ExecutionModeRayTrain
		if err := spec.ValidateShape(); err != nil {
			t.Fatalf("client must defer capacity for %d x %d: %v", shape.workers, shape.gpus, err)
		}
		if err := spec.Validate(); err == nil {
			t.Fatalf("server must retain configured capacity for %d x %d", shape.workers, shape.gpus)
		}
	}
	if got := CurrentResourceLimits(); got != before {
		t.Fatalf("shape validation changed server limits: got %+v, want %+v", got, before)
	}
	SetResourceLimits(ResourceLimits{MaxWorkerReplicas: 4, MaxGPUsPerWorker: 16, MaxTotalGPUs: 32})
	if err := specWithResources(4, 8).Validate(); err != nil {
		t.Fatalf("server must accept expanded capacity: %v", err)
	}
	if err := specWithResources(2, 16).Validate(); err != nil {
		t.Fatalf("server must accept larger GPU nodes: %v", err)
	}
	if err := specWithResources(3, 16).Validate(); err == nil || !strings.Contains(err.Error(), "total GPUs cannot exceed 32") {
		t.Fatalf("server must retain independent total-GPU ceiling: %v", err)
	}
	if err := UpdateResourceLimitsFromCapacity(0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := specWithResources(4, 8).ValidateShape(); err != nil {
		t.Fatalf("offline validation must not depend on an empty server pool: %v", err)
	}
	if err := specWithResources(1, 1).Validate(); err == nil {
		t.Fatal("server must reject GPU jobs when the pool is empty")
	}
}

func TestJobSpecShapeRejectsInvalidResourceCounts(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name      string
		workers   int
		gpus      int
		wantError string
	}{
		{name: "zero workers", workers: 0, gpus: 1, wantError: "workerReplicas must be positive"},
		{name: "negative workers", workers: -1, gpus: 1, wantError: "workerReplicas must be positive"},
		{name: "zero GPUs", workers: 1, gpus: 0, wantError: "gpusPerWorker must be positive"},
		{name: "negative GPUs", workers: 1, gpus: -1, wantError: "gpusPerWorker must be positive"},
		{name: "GPU multiplication overflow", workers: maxInt, gpus: 2, wantError: "total GPUs exceeds supported integer limits"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := specWithResources(test.workers, test.gpus).ValidateShape()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected %q, got %v", test.wantError, err)
			}
		})
	}
	if err := specWithResources(maxInt, 1).ValidateShape(); err != nil {
		t.Fatalf("largest representable GPU count is not an overflow: %v", err)
	}
	SetResourceLimits(ResourceLimits{MaxWorkerReplicas: maxInt, MaxGPUsPerWorker: maxInt, MaxTotalGPUs: maxInt})
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	if err := specWithResources(maxInt, 2).Validate(); err == nil || !strings.Contains(err.Error(), "supported integer limits") {
		t.Fatalf("server validation must also prevent multiplication overflow: %v", err)
	}
}

func TestJobSpecShapePreservesSharedValidation(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*JobSpec)
		wantError string
	}{
		{name: "invalid name", change: func(s *JobSpec) { s.Name = "../escape" }, wantError: "name must be a lowercase DNS label"},
		{name: "unversioned image", change: func(s *JobSpec) { s.Image = "registry.example/train" }, wantError: "image must include"},
		{name: "missing entrypoint", change: func(s *JobSpec) { s.Entrypoint = Entrypoint{} }, wantError: "entrypoint command is required"},
		{name: "incompatible execution", change: func(s *JobSpec) { s.Execution.Mode = ExecutionModeSingleGPU }, wantError: "single_gpu requires"},
		{name: "invalid retry", change: func(s *JobSpec) { s.RetryPolicy.MaxRetries = -1 }, wantError: "maxRetries must be between"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := specWithResources(4, 8)
			test.change(&spec)
			err := spec.ValidateShape()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected unchanged shared validation %q, got %v", test.wantError, err)
			}
		})
	}
}

// Growing the cluster past three machines must be a configuration change, not
// a rebuild: the ceilings come from the platform's configured capacity.
func TestResourceLimitsAreConfigurable(t *testing.T) {
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	SetResourceLimits(ResourceLimits{MaxWorkerReplicas: 8, MaxGPUsPerWorker: 8, MaxTotalGPUs: 64})

	if err := specWithResources(8, 8).Validate(); err != nil {
		t.Fatalf("8 x 8 = 64 GPUs must be allowed after raising the limits: %v", err)
	}
	if err := specWithResources(9, 8).Validate(); err == nil {
		t.Fatalf("expected the raised worker ceiling to still apply")
	}
}

func TestTotalGPUCeilingIsEnforcedIndependently(t *testing.T) {
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	SetResourceLimits(ResourceLimits{MaxWorkerReplicas: 8, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})

	if err := specWithResources(4, 8).Validate(); err == nil {
		t.Fatalf("32 GPUs must be rejected when the total ceiling is 16")
	}
	if err := specWithResources(2, 8).Validate(); err != nil {
		t.Fatalf("16 GPUs must be allowed at the ceiling: %v", err)
	}
}

func TestPartialResourceLimitsFallBackToDefaults(t *testing.T) {
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	SetResourceLimits(ResourceLimits{MaxTotalGPUs: 48})

	// Only the total was raised, so the per-job worker ceiling still applies.
	if err := specWithResources(4, 8).Validate(); err == nil {
		t.Fatalf("unset limits must keep their defaults")
	}
}

func TestUpdateResourceLimitsFromCapacityUsesValidObservation(t *testing.T) {
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	SetResourceLimits(ResourceLimits{MaxWorkerReplicas: 3, MaxGPUsPerWorker: 8, MaxTotalGPUs: 24})

	if err := UpdateResourceLimitsFromCapacity(5, 6, 32); err != nil {
		t.Fatalf("update limits from observed capacity: %v", err)
	}

	got := CurrentResourceLimits()
	want := ResourceLimits{MaxWorkerReplicas: 5, MaxGPUsPerWorker: 6, MaxTotalGPUs: 32}
	if got != want {
		t.Fatalf("expected runtime limits %+v, got %+v", want, got)
	}
}

func TestUpdateResourceLimitsFromCapacityPreservesLastKnownGood(t *testing.T) {
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	want := ResourceLimits{MaxWorkerReplicas: 5, MaxGPUsPerWorker: 4, MaxTotalGPUs: 20}

	invalidObservations := []struct {
		name                    string
		readyNodes              int
		guaranteedGPUsPerWorker int64
		totalGPUs               int64
	}{
		{name: "negative nodes", readyNodes: -1},
		{name: "negative GPU guarantee", guaranteedGPUsPerWorker: -1},
		{name: "negative total GPUs", totalGPUs: -1},
		{name: "zero nodes", readyNodes: 0, guaranteedGPUsPerWorker: 4, totalGPUs: 20},
		{name: "zero per-node GPUs", readyNodes: 5, guaranteedGPUsPerWorker: 0, totalGPUs: 20},
		{name: "zero total GPUs", readyNodes: 5, guaranteedGPUsPerWorker: 4, totalGPUs: 0},
		{name: "per-node exceeds total", readyNodes: 1, guaranteedGPUsPerWorker: 21, totalGPUs: 20},
		{name: "total cannot cover guaranteed worker shape", readyNodes: 3, guaranteedGPUsPerWorker: 8, totalGPUs: 14},
		{name: "worker shape multiplication would overflow", readyNodes: 2, guaranteedGPUsPerWorker: int64(^uint64(0) >> 1), totalGPUs: int64(^uint64(0) >> 1)},
	}
	for _, observation := range invalidObservations {
		t.Run(observation.name, func(t *testing.T) {
			SetResourceLimits(ResourceLimits{MaxWorkerReplicas: 3, MaxGPUsPerWorker: 8, MaxTotalGPUs: 24})
			if err := UpdateResourceLimitsFromCapacity(5, 4, 20); err != nil {
				t.Fatalf("seed last-known-good observed capacity: %v", err)
			}
			if err := UpdateResourceLimitsFromCapacity(observation.readyNodes, observation.guaranteedGPUsPerWorker, observation.totalGPUs); err == nil {
				t.Fatalf("expected invalid capacity to be rejected")
			}
			if got := CurrentResourceLimits(); got != want {
				t.Fatalf("invalid capacity replaced last-known-good limits: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestUpdateResourceLimitsFromEmptyCapacityBlocksGPUJobsAndRecovers(t *testing.T) {
	t.Cleanup(func() { SetResourceLimits(ResourceLimits{}) })
	SetResourceLimits(ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	if err := UpdateResourceLimitsFromCapacity(0, 0, 0); err != nil {
		t.Fatalf("successful empty observation: %v", err)
	}
	if got := CurrentResourceLimits(); got != (ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 0}) {
		t.Fatalf("empty capacity must zero total while retaining valid structural ceilings: %+v", got)
	}
	if err := specWithResources(1, 1).Validate(); err == nil {
		t.Fatal("empty pool admitted a GPU job")
	}
	if err := UpdateResourceLimitsFromCapacity(1, 8, 8); err != nil {
		t.Fatal(err)
	}
	if err := specWithResources(1, 8).Validate(); err != nil {
		t.Fatalf("recovered pool must accept GPU jobs: %v", err)
	}
}
