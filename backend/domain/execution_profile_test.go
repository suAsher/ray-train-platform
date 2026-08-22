package domain

import "testing"

func TestExecutionProfileValidatesSingleNodeDDP(t *testing.T) {
	err := (ExecutionProfile{Mode: ExecutionModeTorchrun}).Validate(Resources{WorkerReplicas: 1, GPUsPerWorker: 2})
	if err != nil {
		t.Fatalf("validate single-node DDP: %v", err)
	}
}

func TestExecutionProfileRejectsMultiNodeTorchrun(t *testing.T) {
	err := (ExecutionProfile{Mode: ExecutionModeTorchrun}).Validate(Resources{WorkerReplicas: 2, GPUsPerWorker: 2})
	if err == nil {
		t.Fatal("expected multi-node torchrun rejection")
	}
}

func TestExecutionProfileAllowsMultiNodeMultiGPU(t *testing.T) {
	err := (ExecutionProfile{Mode: ExecutionModeRayTrain}).Validate(Resources{WorkerReplicas: 2, GPUsPerWorker: 8})
	if err != nil {
		t.Fatalf("validate two-node eight-GPU DDP: %v", err)
	}
}

func TestExecutionProfileKeepsExistingJobsCompatible(t *testing.T) {
	if mode := (ExecutionProfile{}).ResolvedMode(); mode != ExecutionModeLegacy {
		t.Fatalf("empty execution profile = %q, want legacy", mode)
	}
}
