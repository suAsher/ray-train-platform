package domain

import (
	"strings"
	"testing"
)

func TestTrainingEngineDefaultsOldJobsToRayDDP(t *testing.T) {
	if got := (TrainingEngine("")).Resolved(); got != TrainingEngineRayDDP {
		t.Fatalf("old job resolved to %q", got)
	}
}

func TestTrainingEngineDefaultsWhitespaceToRayDDP(t *testing.T) {
	if got := (TrainingEngine(" \t\n")).Resolved(); got != TrainingEngineRayDDP {
		t.Fatalf("whitespace engine resolved to %q", got)
	}
}

func TestManagedTrainingPolicyIsBounded(t *testing.T) {
	policy := ManagedTrainingPolicy{
		MaxFailures: 2,
		Checkpoint:  CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedTrainingPolicyAcceptsEveryUpperBoundary(t *testing.T) {
	policy := ManagedTrainingPolicy{
		MaxFailures: 10,
		Checkpoint:  CheckpointPolicy{EveryEpochs: 100000, KeepLatest: 1000, KeepBest: 1000},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("upper boundaries must remain valid: %v", err)
	}
}

func TestManagedTrainingPolicyRejectsValuesOutsideBounds(t *testing.T) {
	tests := []struct {
		name    string
		policy  ManagedTrainingPolicy
		wantErr string
	}{
		{name: "negative max failures", policy: ManagedTrainingPolicy{MaxFailures: -1}, wantErr: "maxFailures"},
		{name: "too many failures", policy: ManagedTrainingPolicy{MaxFailures: 11}, wantErr: "maxFailures"},
		{name: "negative checkpoint frequency", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{EveryEpochs: -1}}, wantErr: "everyEpochs"},
		{name: "too many checkpoint epochs", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{EveryEpochs: 100001}}, wantErr: "everyEpochs"},
		{name: "negative latest retention", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepLatest: -1}}, wantErr: "keepLatest"},
		{name: "too many latest checkpoints", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepLatest: 1001}}, wantErr: "keepLatest"},
		{name: "negative best retention", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepBest: -1}}, wantErr: "keepBest"},
		{name: "too many best checkpoints", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepBest: 1001}}, wantErr: "keepBest"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.policy.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}
