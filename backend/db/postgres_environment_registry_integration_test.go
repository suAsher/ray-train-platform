package db

import (
	"testing"

	"gorm.io/gorm"
)

func TestPostgresEnvironmentRegistryHostMigrationUpgradeAndLegacyWrites(t *testing.T) {
	database := openPostgresTestSchema(t)
	applyPostgresMigrationsThrough(t, database, 56)
	insertLegacyEnvironmentRegistryRows(t, database, "before-upgrade")
	before := map[string]string{}
	for _, table := range []string{"environment_registry_authorizations", "environment_builds"} {
		var row string
		if err := database.Raw("SELECT to_jsonb(record)::text FROM "+table+" AS record WHERE id = ?", "before-upgrade").Scan(&row).Error; err != nil {
			t.Fatalf("read legacy %s: %v", table, err)
		}
		before[table] = row
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ApplyMigrations(database); err != nil {
			t.Fatalf("upgrade attempt %d: %v", attempt, err)
		}
	}
	assertEnvironmentRegistryHostColumns(t, database)
	for table, row := range before {
		var unchanged bool
		if err := database.Raw("SELECT (to_jsonb(record) - 'registry_host') = ?::jsonb FROM "+table+" AS record WHERE id = ?", row, "before-upgrade").Scan(&unchanged).Error; err != nil || !unchanged {
			t.Fatalf("migration changed existing %s metadata: unchanged=%t error=%v", table, unchanged, err)
		}
	}
	// The previous backend omits registry_host on inserts and updates. Keep
	// those writes valid so application rollback does not need a down migration.
	insertLegacyEnvironmentRegistryRows(t, database, "legacy-after-upgrade")
	for _, table := range []string{"environment_registry_authorizations", "environment_builds"} {
		var hosts []string
		if err := database.Table(table).Order("id").Pluck("registry_host", &hosts).Error; err != nil {
			t.Fatal(err)
		}
		if len(hosts) != 2 || hosts[0] != "harbor.wellspiking.ai" || hosts[1] != "harbor.wellspiking.ai" {
			t.Fatalf("legacy %s rows did not retain original Harbor: %v", table, hosts)
		}
		if err := database.Table(table).Where("id = ?", "legacy-after-upgrade").Update("registry_host", "harbor.qomolo.com").Error; err != nil {
			t.Fatalf("persist second Harbor for %s: %v", table, err)
		}
	}
	if err := database.Exec("UPDATE environment_registry_authorizations SET build_id = ? WHERE id = ?", "legacy-build-bound", "legacy-after-upgrade").Error; err != nil {
		t.Fatalf("legacy authorization update failed: %v", err)
	}
	if err := database.Exec("UPDATE environment_builds SET message = ? WHERE id = ?", "legacy write", "legacy-after-upgrade").Error; err != nil {
		t.Fatalf("legacy build update failed: %v", err)
	}
	for _, table := range []string{"environment_registry_authorizations", "environment_builds"} {
		var host string
		if err := database.Raw("SELECT registry_host FROM "+table+" WHERE id = ?", "legacy-after-upgrade").Scan(&host).Error; err != nil || host != "harbor.qomolo.com" {
			t.Fatalf("legacy update lost host on %s: host=%q error=%v", table, host, err)
		}
	}
}

func assertEnvironmentRegistryHostColumns(t *testing.T, database *gorm.DB) {
	t.Helper()
	for _, table := range []string{"environment_registry_authorizations", "environment_builds"} {
		var column struct {
			ColumnDefault string
			IsNullable    string
		}
		if err := database.Raw(`SELECT column_default, is_nullable FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'registry_host'`, table).Scan(&column).Error; err != nil {
			t.Fatal(err)
		}
		if column.ColumnDefault != "'harbor.wellspiking.ai'::text" || column.IsNullable != "NO" {
			t.Fatalf("%s registry host column default=%q nullable=%q", table, column.ColumnDefault, column.IsNullable)
		}
	}
}

func insertLegacyEnvironmentRegistryRows(t *testing.T, database *gorm.DB, id string) {
	t.Helper()
	if err := database.Exec(`INSERT INTO environment_registry_authorizations
(id, tenant_id, owner_id, username, secret_ref, target, expires_at, created_at)
VALUES (?, 'legacy-team', 'legacy-owner', 'legacy-registry-user', 'test-material-reference', 'harbor.wellspiking.ai/public/environment:legacy', '2027-01-01T00:00:00Z', '2026-09-01T00:00:00Z')`, id).Error; err != nil {
		t.Fatalf("insert legacy authorization: %v", err)
	}
	if err := database.Exec(`INSERT INTO environment_builds
(id, tenant_id, owner_id, workspace_id, namespace, workspace_resource_name, workspace_uid,
base_image, workspace_image, name, description, visibility, project, repository, tag,
status, resume_status, auth_id, idempotency_key, snapshot_json, artifact_digest,
image_digest, image_reference, checks_json, attempt, artifact_expires_at, created_at, updated_at)
VALUES (?, 'legacy-team', 'legacy-owner', 'legacy-workspace', 'legacy-namespace', 'legacy-resource', 'legacy-uid',
'legacy-base', 'legacy-workspace-image', 'Legacy environment', 'Keep metadata', 'personal', 'public', 'environment', ?,
'FAILED', 'PUSHING', ?, ?, '{"schemaVersion":1}', 'sha256:legacy-artifact',
'sha256:legacy-image', 'harbor.wellspiking.ai/public/environment@sha256:legacy-image', '{"cpu":true}', 2,
'2027-01-01T00:00:00Z', '2026-09-01T00:00:00Z', '2026-09-02T00:00:00Z')`, id, id, id, id).Error; err != nil {
		t.Fatalf("insert legacy build: %v", err)
	}
}
