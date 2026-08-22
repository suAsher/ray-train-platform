package domain

import "testing"

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
