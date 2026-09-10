package domain

import (
	"fmt"
	"strings"
)

type AcceleratorClass string

const (
	AcceleratorRTX4090 AcceleratorClass = "rtx4090"
	AcceleratorA100    AcceleratorClass = "a100"
	AcceleratorA800    AcceleratorClass = "a800"
	AcceleratorH20     AcceleratorClass = "h20"
)

func (accelerator AcceleratorClass) Resolved() AcceleratorClass {
	if strings.TrimSpace(string(accelerator)) == "" {
		return AcceleratorRTX4090
	}
	return AcceleratorClass(strings.ToLower(strings.TrimSpace(string(accelerator))))
}

func (accelerator AcceleratorClass) Validate() error {
	switch accelerator.Resolved() {
	case AcceleratorRTX4090, AcceleratorA100, AcceleratorA800, AcceleratorH20:
		return nil
	default:
		return fmt.Errorf("unsupported accelerator class %q", accelerator)
	}
}

func (accelerator AcceleratorClass) NodeLabelValue() string {
	if accelerator.Resolved() == AcceleratorRTX4090 {
		return "nvidia-rtx-4090"
	}
	return "nvidia-" + string(accelerator.Resolved())
}

type WorkloadPriority string

const (
	WorkloadPriorityProduction    WorkloadPriority = "production"
	WorkloadPriorityNormal        WorkloadPriority = "normal"
	WorkloadPriorityOpportunistic WorkloadPriority = "opportunistic"
)

func (priority WorkloadPriority) Resolved() WorkloadPriority {
	if strings.TrimSpace(string(priority)) == "" {
		return WorkloadPriorityNormal
	}
	return WorkloadPriority(strings.ToLower(strings.TrimSpace(string(priority))))
}

func (priority WorkloadPriority) Validate() error {
	switch priority.Resolved() {
	case WorkloadPriorityProduction, WorkloadPriorityNormal, WorkloadPriorityOpportunistic:
		return nil
	default:
		return fmt.Errorf("unsupported workload priority %q", priority)
	}
}

func (priority WorkloadPriority) KubernetesPriorityClass() string {
	return "raytrain-" + string(priority.Resolved())
}

func (spec JobSpec) HasRecoverableCheckpointPolicy() bool {
	return spec.TrainingEngine.Resolved() == TrainingEngineRayTrain &&
		spec.Managed.MaxFailures > 0 &&
		spec.Managed.Checkpoint.EveryEpochs > 0 &&
		(spec.Managed.Checkpoint.KeepLatest > 0 || spec.Managed.Checkpoint.KeepBest > 0)
}

func (spec JobSpec) validateScheduling() error {
	if err := spec.AcceleratorClass.Validate(); err != nil {
		return err
	}
	priority := WorkloadPriority(spec.Priority)
	if err := priority.Validate(); err != nil {
		return err
	}
	if spec.Preemptible && priority.Resolved() != WorkloadPriorityOpportunistic {
		return fmt.Errorf("preemptible jobs must use opportunistic priority")
	}
	if priority.Resolved() == WorkloadPriorityOpportunistic {
		if !spec.Preemptible {
			return fmt.Errorf("opportunistic jobs must explicitly enable preemption")
		}
		if !spec.HasRecoverableCheckpointPolicy() {
			return fmt.Errorf("opportunistic jobs require managed Ray Train checkpoint recovery")
		}
	}
	return nil
}
