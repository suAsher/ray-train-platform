package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestPostgresWarehouseSyncUpgradeFrom52PreservesExistingRows(t *testing.T) {
	database := openPostgresTestSchema(t)
	older := fstest.MapFS{}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() < "0053" {
			name := "migrations/" + entry.Name()
			content, err := migrationFiles.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			older[name] = &fstest.MapFile{Data: content}
		}
	}
	if err := applyMigrations(database, older); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := database.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 52 {
		t.Fatalf("before schema %d %v", version, err)
	}
	seedPostgresIdentityRows(t, database)
	if err := database.Exec("INSERT INTO model_catalog(id,name,owner_id,tenant_id) VALUES ('existing-model','existing model','user-a1','tenant-a')").Error; err != nil {
		t.Fatal(err)
	}
	var originalIdentity, originalModel string
	if err := database.Raw("SELECT to_jsonb(u)::text FROM local_users u WHERE id = 'user-a1'").Scan(&originalIdentity).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Raw("SELECT to_jsonb(m)::text FROM model_catalog m WHERE id = 'existing-model'").Scan(&originalModel).Error; err != nil {
		t.Fatal(err)
	}
	// Retry the full migration runner, as multiple backend replicas do at startup.
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != 54 {
		t.Fatalf("after schema %d %v", version, err)
	}
	var actualIdentity, actualModel string
	if err := database.Raw("SELECT to_jsonb(u)::text FROM local_users u WHERE id = 'user-a1'").Scan(&actualIdentity).Error; err != nil || actualIdentity != originalIdentity {
		t.Fatal("existing identity changed", err)
	}
	if err := database.Raw("SELECT to_jsonb(m)::text FROM model_catalog m WHERE id = 'existing-model'").Scan(&actualModel).Error; err != nil || actualModel != originalModel {
		t.Fatal("existing model changed", err)
	}
	var count int64
	if err := database.Table("function_warehouse_syncs").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("migration created unsolicited exports: %d %v", count, err)
	}
	// The schema-52 model write shape remains valid after the additive migration.
	if err := database.Exec("INSERT INTO model_catalog(id,name,owner_id,tenant_id) VALUES ('old-backend-model','old backend','user-a1','tenant-a')").Error; err != nil {
		t.Fatal("schema-52 writer incompatible", err)
	}
	insert := `INSERT INTO function_warehouse_syncs(id,owner_id,tenant_id,job_id,environment,warehouse_id,model_type_id,version,paths,state,idempotency_key,request_sha256,credential)
 VALUES (?, 'user-a1','tenant-a','job-example','development','warehouse','model-type','v1','["best.pth"]'::jsonb,'QUEUED',?,?,decode('010203','hex'))`
	if err := database.Exec(insert, "sync-one", "intent-one", strings.Repeat("a", 64)).Error; err != nil {
		t.Fatal(err)
	}
	// Explicit new intent is allowed even with identical job, model and file.
	if err := database.Exec(insert, "sync-two", "intent-two", strings.Repeat("a", 64)).Error; err != nil {
		t.Fatal("new intentional repeat rejected", err)
	}
	if err := database.Exec(insert, "sync-duplicate", "intent-one", strings.Repeat("a", 64)).Error; err == nil {
		t.Fatal("same intent duplicated")
	}
	if err := database.Exec("UPDATE function_warehouse_syncs SET job_id = 'different-job' WHERE id = 'sync-one'").Error; err == nil {
		t.Fatal("frozen source changed")
	}
	if err := database.Exec("UPDATE function_warehouse_syncs SET state = 'CANCELED', credential = NULL WHERE id = 'sync-one'").Error; err != nil {
		t.Fatal("allowed control state rejected", err)
	}
	var erased bool
	if err := database.Raw("SELECT credential IS NULL FROM function_warehouse_syncs WHERE id = 'sync-one'").Scan(&erased).Error; err != nil || !erased {
		t.Fatal("credential not erased", err)
	}
}
