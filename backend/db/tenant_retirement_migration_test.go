package db

import (
	"strings"
	"testing"
)

func TestTenantRetirementMigrationPreservesIdentityAndFencesWrites(t *testing.T) {
	data, err := migrationFiles.ReadFile("migrations/0034_tenant_retirement.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, want := range []string{"ADD COLUMN retired_at", "ADD COLUMN retired_by", "pg_advisory_xact_lock_shared", "FOR SHARE", "BEFORE INSERT OR UPDATE OR DELETE", "retired tenant identity is permanent", "dataset_publication_runs", "owner_tenant_id", "training_job_event_tokens", "training_job_events", "managed_attempt_resources", "managed_attempt_fences", "dataset_cache_observations", "data_space_upload_parts"} {
		if !strings.Contains(sql, want) {
			t.Errorf("migration missing %q", want)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "DROP TABLE") {
		t.Fatal("retirement must preserve history")
	}
}
