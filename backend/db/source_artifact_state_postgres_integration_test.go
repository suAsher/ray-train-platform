package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestPostgresSourceArtifactV1StateMigrationRefusesLegacyRowsWithoutMutation(t *testing.T) {
	database := openPostgresTestSchema(t)
	firstTwo := fstest.MapFS{}
	for _, name := range []string{"migrations/0001_initial.up.sql", "migrations/0002_submission_gateway.up.sql"} {
		contents, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		firstTwo[name] = &fstest.MapFile{Data: contents}
	}
	if err := applyMigrations(database, firstTwo); err != nil {
		t.Fatalf("apply first two migrations: %v", err)
	}
	seedPostgresIdentityRows(t, database)
	digest := strings.Repeat("b", 64)
	if err := database.Exec(`
INSERT INTO source_artifacts(id, tenant_id, user_id, sha256, size_bytes, object_key, state, upload_expires_at)
VALUES ('legacy-failed', 'tenant-a', 'user-a1', ?, 1, 'legacy.zip', 'FAILED', NOW() + INTERVAL '1 hour')`, digest).Error; err != nil {
		t.Fatalf("insert legacy FAILED artifact: %v", err)
	}

	err := ApplyMigrations(database)
	if err == nil || !strings.Contains(err.Error(), "state migration refused") {
		t.Fatalf("migration must clearly refuse legacy states, got %v", err)
	}
	var versionCount int64
	if err := database.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version = 3").Scan(&versionCount).Error; err != nil {
		t.Fatalf("check migration version: %v", err)
	}
	if versionCount != 0 {
		t.Fatal("failed migration 3 was recorded as applied")
	}
	var state string
	if err := database.Raw("SELECT state FROM source_artifacts WHERE id = 'legacy-failed'").Scan(&state).Error; err != nil {
		t.Fatalf("reload legacy artifact: %v", err)
	}
	if state != "FAILED" {
		t.Fatalf("legacy state was mutated to %q", state)
	}

	if err := database.Exec("DELETE FROM source_artifacts WHERE id = 'legacy-failed'").Error; err != nil {
		t.Fatalf("remove test fixture: %v", err)
	}
	if err := ApplyMigrations(database); err != nil {
		t.Fatalf("apply migration after explicitly resolving legacy row: %v", err)
	}
	if err := database.Exec(`
INSERT INTO source_artifacts(id, tenant_id, user_id, sha256, size_bytes, object_key, state, upload_expires_at)
VALUES ('new-failed', 'tenant-a', 'user-a1', ?, 1, 'new.zip', 'FAILED', NOW() + INTERVAL '1 hour')`, digest).Error; err == nil {
		t.Fatal("two-state V1 constraint accepted FAILED")
	}
}
