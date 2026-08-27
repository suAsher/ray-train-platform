package domain

import (
	"fmt"
	"strings"
)

type TrainingEngine string

const (
	TrainingEngineRayDDP   TrainingEngine = "ray-ddp"
	TrainingEngineRayTrain TrainingEngine = "ray-train"

	RayVersionLegacy     = "2.35.0"
	RayVersionProduction = "2.56.1"
	RayVersionCanary     = "2.58.0"

	ManagedMaxFailuresLimit           = 10
	ManagedCheckpointEveryEpochsLimit = 100000
	ManagedCheckpointRetentionLimit   = 1000
)

func (engine TrainingEngine) Resolved() TrainingEngine {
	if strings.TrimSpace(string(engine)) == "" {
		return TrainingEngineRayDDP
	}
	return engine
}

type DataMode string

const (
	DataModeMount   DataMode = "mount"
	DataModeCache   DataMode = "cache"
	DataModeRayData DataMode = "ray-data"
)

type CheckpointPolicy struct {
	EveryEpochs int `json:"everyEpochs,omitempty"`
	KeepLatest  int `json:"keepLatest,omitempty"`
	KeepBest    int `json:"keepBest,omitempty"`
}

type ManagedTrainingPolicy struct {
	MaxFailures int              `json:"maxFailures,omitempty"`
	Checkpoint  CheckpointPolicy `json:"checkpoint,omitempty"`
}

func (policy ManagedTrainingPolicy) Validate() error {
	if policy.MaxFailures < 0 || policy.MaxFailures > ManagedMaxFailuresLimit {
		return fmt.Errorf("maxFailures must be between 0 and %d", ManagedMaxFailuresLimit)
	}
	if policy.Checkpoint.EveryEpochs < 0 || policy.Checkpoint.EveryEpochs > ManagedCheckpointEveryEpochsLimit {
		return fmt.Errorf("checkpoint.everyEpochs must be between 0 and %d", ManagedCheckpointEveryEpochsLimit)
	}
	if policy.Checkpoint.KeepLatest < 0 || policy.Checkpoint.KeepLatest > ManagedCheckpointRetentionLimit {
		return fmt.Errorf("checkpoint.keepLatest must be between 0 and %d", ManagedCheckpointRetentionLimit)
	}
	if policy.Checkpoint.KeepBest < 0 || policy.Checkpoint.KeepBest > ManagedCheckpointRetentionLimit {
		return fmt.Errorf("checkpoint.keepBest must be between 0 and %d", ManagedCheckpointRetentionLimit)
	}
	return nil
}
