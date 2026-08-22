package domain

import "fmt"

// ExecutionMode is the execution contract selected by a user.  Resources say
// how much GPU to reserve; the mode says where and how the user command runs.
// Keeping the two separate prevents a multi-GPU request from silently running
// the command on the CPU-only Ray head.
type ExecutionMode string

const (
	// ExecutionModeLegacy preserves already-created API records. New Portal and
	// spk-rayjob submissions always send one of the explicit modes below.
	ExecutionModeLegacy    ExecutionMode = "legacy"
	ExecutionModeSingleGPU ExecutionMode = "single_gpu"
	ExecutionModeTorchrun  ExecutionMode = "torchrun"
	ExecutionModeRayTrain  ExecutionMode = "ray_train"
)

type ExecutionProfile struct {
	Mode ExecutionMode `json:"mode,omitempty"`
}

func (profile ExecutionProfile) ResolvedMode() ExecutionMode {
	if profile.Mode == "" {
		return ExecutionModeLegacy
	}
	return profile.Mode
}

func (profile ExecutionProfile) Validate(resources Resources) error {
	switch profile.ResolvedMode() {
	case ExecutionModeLegacy:
		return nil
	case ExecutionModeSingleGPU:
		if resources.WorkerReplicas != 1 || resources.GPUsPerWorker != 1 {
			return fmt.Errorf("single_gpu requires 1 worker with 1 GPU")
		}
	case ExecutionModeTorchrun:
		if resources.WorkerReplicas != 1 || resources.GPUsPerWorker < 2 {
			return fmt.Errorf("torchrun requires 1 worker with at least 2 GPUs")
		}
	case ExecutionModeRayTrain:
		// The launcher allocates one Ray worker Pod per physical DDP node and
		// starts torchrun inside it. Each Pod therefore keeps its complete local
		// GPU set while Ray enforces the cross-node placement contract.
		if resources.WorkerReplicas < 2 || resources.GPUsPerWorker < 1 {
			return fmt.Errorf("ray_train requires at least 2 workers with at least 1 GPU each")
		}
	default:
		return fmt.Errorf("unsupported execution mode %q", profile.Mode)
	}
	return nil
}
