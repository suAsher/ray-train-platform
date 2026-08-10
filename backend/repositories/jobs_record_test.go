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
