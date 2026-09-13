package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestModelReleaseMigrationContract(t *testing.T) {
	b, err := migrationFiles.ReadFile("migrations/0051_model_releases.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, fragment := range []string{"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '60s';", "CREATE TABLE IF NOT EXISTS model_releases", "CREATE TABLE IF NOT EXISTS model_publications", "CREATE TABLE IF NOT EXISTS model_publication_history", "CREATE TABLE IF NOT EXISTS model_registry_links", "reviewer_id<>applicant_id AND reviewer_id<>model_owner_id", "UNIQUE (applicant_id,tenant_id,idempotency_key)", "UNIQUE(model_id,actor_id,idempotency_key)", "decided release is immutable", "release audit history is append only", "publication must reference an approved release"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	if strings.Contains(sql, "ON DELETE CASCADE") || strings.Contains(sql, "REFERENCES users") {
		t.Fatal("stable release history tied to identity deletion")
	}
}

func TestModelServingMigrationContract(t *testing.T) {
	b, err := migrationFiles.ReadFile("migrations/0052_model_serving.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, fragment := range []string{
		"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '60s';",
		"CREATE TABLE IF NOT EXISTS model_serving_contracts",
		"CREATE TABLE IF NOT EXISTS model_serving_deployments",
		"CREATE TABLE IF NOT EXISTS model_serving_audits",
		"job_id ~ '^job-[0-9a-f]{24}$'",
		"model_serving_one_active_idx",
		"to_jsonb(OLD) - ARRAY['active','revision']",
		"serving contract executable snapshot is immutable",
		"CREATE TRIGGER model_serving_contracts_immutable BEFORE UPDATE ON model_serving_contracts",
		"to_jsonb(OLD) - ARRAY['state','error','revision','updated_at']",
		"serving provenance and reserved job identity are immutable",
		"terminal serving deployment is immutable",
		"serving deployment state cannot regress",
		"CREATE TRIGGER model_serving_deployments_immutable BEFORE UPDATE ON model_serving_deployments",
		"serving audit history is append only",
		"CREATE TRIGGER model_serving_audits_append_only BEFORE UPDATE OR DELETE ON model_serving_audits",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
}

func TestPostgresModelReleaseUpgradeFrom50(t *testing.T) {
	database := openPostgresTestSchema(t)
	older := fstest.MapFS{}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() < "0051" {
			name := "migrations/" + entry.Name()
			b, err := migrationFiles.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			older[name] = &fstest.MapFile{Data: b}
		}
	}
	if err := applyMigrations(database, older); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := database.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 50 {
		t.Fatalf("before schema %d %v", version, err)
	}
	if err := database.Exec("INSERT INTO model_catalog(id,name,owner_id,tenant_id) VALUES ('preserved-model','existing model','stable-owner','old-team')").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(database); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := database.Table("model_catalog").Where("id = ? AND owner_id = ? AND tenant_id = ?", "preserved-model", "stable-owner", "old-team").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("existing ownership changed %d %v", count, err)
	}
	if err := database.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 52 {
		t.Fatalf("after schema %d %v", version, err)
	}
	for _, table := range []string{"model_releases", "model_publications", "model_publication_history", "model_registry_links"} {
		if err := database.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("unexpected %s rows %d %v", table, count, err)
		}
	}
}
