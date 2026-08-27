package repositories

import (
	"encoding/json"
	"testing"

	"ray-train-platform-backend/domain"
)

// The training_jobs table stores cleanup_json as jsonb. SQLite accepts any
// string there, so this asserts the mapping directly: an empty string is not
// valid JSON and PostgreSQL rejects the whole INSERT with SQLSTATE 22P02,
// which makes every job submission fail in a real deployment.
func TestNewJobRecordAlwaysWritesValidJSONColumns(t *testing.T) {
	job := testJob()
	job.Spec.CleanupPolicy = domain.CleanupPolicy{}

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatalf("build job record: %v", err)
	}

	for name, value := range map[string]string{"cleanup_json": record.CleanupJSON, "spec_json": record.SpecJSON} {
		if value == "" {
			t.Fatalf("%s must never be empty, jsonb rejects it", name)
		}
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			t.Fatalf("%s must be valid JSON, got %q: %v", name, value, err)
		}
	}
}

func TestNewJobRecordPreservesCleanupPolicy(t *testing.T) {
	job := testJob()
	job.Spec.CleanupPolicy = domain.CleanupPolicy{SuccessTTLSeconds: 300, FailureTTLSeconds: 900}

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatalf("build job record: %v", err)
	}

	var policy domain.CleanupPolicy
	if err := json.Unmarshal([]byte(record.CleanupJSON), &policy); err != nil {
		t.Fatalf("decode cleanup policy: %v", err)
	}
	if policy.SuccessTTLSeconds != 300 || policy.FailureTTLSeconds != 900 {
		t.Fatalf("cleanup policy not round-tripped: %+v", policy)
	}
}

func TestJobRecordRoundTripsManagedRuntimeMetadata(t *testing.T) {
	job := testJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.ParentJobID = "job-0123456789abcdef01234567"
	job.ClusterAttempt = 2
	job.WorkerRestartCount = 3
	job.ResumeCheckpointID = "checkpoint-7"

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatal(err)
	}
	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayTrain || got.Spec.RayVersion != domain.RayVersionProduction || got.Spec.ParentJobID != job.Spec.ParentJobID || got.ClusterAttempt != 2 || got.WorkerRestartCount != 3 || got.ResumeCheckpointID != "checkpoint-7" {
		t.Fatalf("runtime metadata lost: %+v", got)
	}
}

func TestJobRecordNormalizedRuntimeMetadataOverridesSpecJSON(t *testing.T) {
	job := testJob()
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}
	record := JobRecord{
		SpecJSON:           string(specJSON),
		TrainingEngine:     string(domain.TrainingEngineRayTrain),
		RayVersion:         domain.RayVersionCanary,
		ClusterAttempt:     4,
		WorkerRestartCount: 5,
		ResumeCheckpointID: "checkpoint-normalized",
		ParentJobID:        "job-89abcdef0123456701234567",
	}

	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayTrain || got.Spec.RayVersion != domain.RayVersionCanary || got.Spec.ParentJobID != record.ParentJobID {
		t.Fatalf("normalized spec metadata did not override spec_json: %+v", got.Spec)
	}
	if got.ClusterAttempt != 4 || got.WorkerRestartCount != 5 || got.ResumeCheckpointID != "checkpoint-normalized" {
		t.Fatalf("normalized runtime counters lost: %+v", got)
	}
}

func TestJobRecordResolvesLegacyRuntimeMetadataDefaults(t *testing.T) {
	job := testJob()
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}

	got, err := (JobRecord{SpecJSON: string(specJSON)}).toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayDDP || got.Spec.RayVersion != domain.RayVersionLegacy || got.ClusterAttempt != 1 {
		t.Fatalf("legacy runtime metadata defaults not resolved: %+v", got)
	}
}

func TestJobRecordDefensivelyNormalizesInvalidRuntimeCounters(t *testing.T) {
	job := testJob()
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}

	got, err := (JobRecord{
		SpecJSON:           string(specJSON),
		ClusterAttempt:     -2,
		WorkerRestartCount: -3,
	}).toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.ClusterAttempt != 1 || got.WorkerRestartCount != 0 {
		t.Fatalf("invalid runtime counters not normalized: %+v", got)
	}
}
