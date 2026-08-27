package domain

import (
	"math"
	"testing"
)

func TestTrainingEventValidateAcceptsOnlyManagedEventTypes(t *testing.T) {
	for _, eventType := range []TrainingEventType{
		TrainingEventWorkerGroupStarted,
		TrainingEventCheckpointComplete,
		TrainingEventProgress,
	} {
		event := TrainingEvent{ID: "event-1", Type: eventType, Generation: 1, Epoch: 1, Step: 1}
		if eventType == TrainingEventCheckpointComplete {
			event.Checkpoint = &TrainingCheckpoint{ID: "checkpoint-1", Epoch: 1, Step: 1, ObjectPath: "/mnt/data/output/.platform/ray-train/job-0123456789abcdef01234567/checkpoints/epoch-1", ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Complete: true}
		}
		if err := event.Validate(); err != nil {
			t.Fatalf("event type %q should be valid: %v", eventType, err)
		}
	}

	for _, eventType := range []TrainingEventType{"", "CHECKPOINT_STARTED", "worker_group_started"} {
		if err := (TrainingEvent{ID: "event-1", Type: eventType, Generation: 1}).Validate(); err == nil {
			t.Fatalf("event type %q should be rejected", eventType)
		}
	}
}

func TestTrainingEventValidateRequiresGenerationForEveryAllowedType(t *testing.T) {
	checkpoint := &TrainingCheckpoint{
		ID: "checkpoint-1", Epoch: 1, Step: 1,
		ObjectPath:     "/mnt/data/output/.platform/ray-train/job-0123456789abcdef01234567/checkpoints/epoch-1",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Complete:       true,
	}
	for _, event := range []TrainingEvent{
		{ID: "worker-no-generation", Type: TrainingEventWorkerGroupStarted},
		{ID: "progress-no-generation", Type: TrainingEventProgress, Epoch: 1, Step: 1},
		{ID: "checkpoint-no-generation", Type: TrainingEventCheckpointComplete, Epoch: 1, Step: 1, Checkpoint: checkpoint},
	} {
		if err := event.Validate(); err == nil {
			t.Fatalf("event type %q accepted an omitted generation", event.Type)
		}
	}
}

func TestTrainingEventValidateBoundsUntrustedFields(t *testing.T) {
	tests := []TrainingEvent{
		{ID: "../event", Type: TrainingEventProgress, Generation: 1},
		{ID: "event-1", Type: TrainingEventProgress, Generation: -1},
		{ID: "event-1", Type: TrainingEventProgress, Generation: 1, Epoch: -1},
		{ID: "event-1", Type: TrainingEventProgress, Generation: 1, Step: -1},
		{ID: "event-1", Type: TrainingEventCheckpointComplete, Generation: 1, Checkpoint: &TrainingCheckpoint{ID: "checkpoint-1", ObjectPath: "/mnt/data/output/checkpoint", MetricValue: floatPointer(math.NaN()), Complete: true}},
	}
	for index, event := range tests {
		if err := event.Validate(); err == nil {
			t.Fatalf("case %d should be rejected: %+v", index, event)
		}
	}
}

func TestTrainingCheckpointValidateRequiresCompleteManifest(t *testing.T) {
	checkpoint := TrainingCheckpoint{
		ID: "checkpoint-1", JobID: "job-0123456789abcdef01234567",
		TenantID: "tenant-a", UserID: "user-a", Epoch: 1, Step: 20,
		ObjectPath:     "/mnt/data/output/.platform/ray-train/job-0123456789abcdef01234567/checkpoints/epoch-1",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Complete:       true,
	}
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
	checkpoint.ManifestSHA256 = "not-a-digest"
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("invalid manifest digest accepted")
	}
}

func floatPointer(value float64) *float64 { return &value }
