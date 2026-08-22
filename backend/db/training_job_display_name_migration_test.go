package db

import (
	"strings"
	"testing"
)

// A training job already has a globally unique platform ID. Its name is a
// user-facing label and must be reusable for the daily edit-submit loop,
// including when an earlier run is archived.
func TestTrainingJobDisplayNameMigrationDropsTenantNameUniqueness(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0017_training_job_display_names.up.sql")
	if err != nil {
		t.Fatalf("read display-name migration: %v", err)
	}
	sql := strings.ToLower(string(contents))
	if !strings.Contains(sql, "alter table training_jobs") || !strings.Contains(sql, "drop constraint if exists training_jobs_tenant_id_name_key") {
		t.Fatalf("migration must drop the legacy tenant/name uniqueness constraint: %s", contents)
	}
	if !strings.Contains(sql, "create index") || !strings.Contains(sql, "training_jobs") || !strings.Contains(sql, "tenant_id, name") {
		t.Fatalf("migration must retain a non-unique tenant/name lookup index: %s", contents)
	}
}
