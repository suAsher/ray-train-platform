package domain

import "testing"

func TestSchedulingDefaultsPreserveExisting4090NormalJobs(t *testing.T) {
	spec := JobSpec{}
	if spec.AcceleratorClass.Resolved() != AcceleratorRTX4090 {
		t.Fatalf("accelerator default=%q", spec.AcceleratorClass.Resolved())
	}
	if WorkloadPriority(spec.Priority).Resolved() != WorkloadPriorityNormal {
		t.Fatalf("priority default=%q", WorkloadPriority(spec.Priority).Resolved())
	}
	if err := spec.validateScheduling(); err != nil {
		t.Fatalf("legacy scheduling defaults must remain valid: %v", err)
	}
}

func TestOpportunisticSchedulingRequiresManagedRecovery(t *testing.T) {
	base := JobSpec{Priority: string(WorkloadPriorityOpportunistic), Preemptible: true, Resources: Resources{WorkerReplicas: 1, GPUsPerWorker: 1}}
	if err := base.validateScheduling(); err == nil {
		t.Fatal("opportunistic job without managed recovery must fail")
	}
	base.TrainingEngine = TrainingEngineRayTrain
	base.Managed = ManagedTrainingPolicy{
		MaxFailures: 1,
		Checkpoint:  CheckpointPolicy{EveryEpochs: 1, KeepLatest: 1},
	}
	if err := base.validateScheduling(); err != nil {
		t.Fatalf("recoverable opportunistic job rejected: %v", err)
	}
	base.Priority = string(WorkloadPriorityNormal)
	if err := base.validateScheduling(); err == nil {
		t.Fatal("normal job must not be marked preemptible")
	}
}

func TestOpportunisticSchedulingRejectsDistributedOrMultiGPUWork(t *testing.T) {
	base := JobSpec{
		Priority: string(WorkloadPriorityOpportunistic), Preemptible: true,
		TrainingEngine: TrainingEngineRayTrain,
		Managed:        ManagedTrainingPolicy{MaxFailures: 1, Checkpoint: CheckpointPolicy{EveryEpochs: 1, KeepLatest: 1}},
		Resources:      Resources{WorkerReplicas: 1, GPUsPerWorker: 1},
	}
	for _, resources := range []Resources{{WorkerReplicas: 2, GPUsPerWorker: 1}, {WorkerReplicas: 1, GPUsPerWorker: 2}} {
		candidate := base
		candidate.Resources = resources
		if err := candidate.validateScheduling(); err == nil {
			t.Fatalf("opportunistic resources %+v must be rejected", resources)
		}
	}
}

func TestAcceleratorClassesMapToStableNodeLabels(t *testing.T) {
	for accelerator, label := range map[AcceleratorClass]string{
		AcceleratorRTX4090: "nvidia-rtx-4090",
		AcceleratorA100:    "nvidia-a100",
		AcceleratorA800:    "nvidia-a800",
		AcceleratorH20:     "nvidia-h20",
	} {
		if got := accelerator.NodeLabelValue(); got != label {
			t.Errorf("accelerator %q label=%q want=%q", accelerator, got, label)
		}
	}
}
