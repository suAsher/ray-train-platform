package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestModelLifecycleMigrationContract(t *testing.T) {
	b, err := migrationFiles.ReadFile("migrations/0049_model_lifecycle.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, fragment := range []string{"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '60s';", "CREATE TABLE IF NOT EXISTS model_catalog", "CREATE TABLE IF NOT EXISTS model_versions", "CREATE TABLE IF NOT EXISTS model_audits", "UNIQUE (model_id, number)", "UNIQUE (model_id, creator_id, idempotency_key)", "21474836480", "'PENDING', 'COPYING', 'READY', 'FAILED'", "model_versions_immutable", "CREATE UNIQUE INDEX IF NOT EXISTS model_catalog_request_idx ON model_catalog(owner_id, idempotency_key) WHERE idempotency_key <> ''", "source_etag TEXT NOT NULL CHECK (length(trim(source_etag)) > 0)"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	if strings.Contains(sql, "REFERENCES users") || strings.Contains(sql, "ON DELETE CASCADE") {
		t.Fatal("model retention tied to membership/source deletion")
	}
}
func TestPostgresModelLifecycleUpgradeFrom48(t *testing.T) {
	db := openPostgresTestSchema(t)
	older := fstest.MapFS{}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() < "0049" {
			name := "migrations/" + entry.Name()
			b, err := migrationFiles.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			older[name] = &fstest.MapFile{Data: b}
		}
	}
	if err := applyMigrations(db, older); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&old).Error; err != nil || old != 48 {
		t.Fatalf("old schema %d %v", old, err)
	}
	// Existing source rows survive the additive migration.
	seedPostgresIdentityRows(t, db)
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(db); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM users WHERE id = 'user-a1'").Scan(&n).Error; err != nil || n != 1 {
		t.Fatalf("identity lost %d %v", n, err)
	}
	if err := db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&old).Error; err != nil || old != 50 {
		t.Fatalf("new schema %d %v", old, err)
	}
}
