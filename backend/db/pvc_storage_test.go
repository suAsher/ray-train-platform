package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestPVCStoragePostgresUpgradePreservesExistingAssets(t *testing.T) {
	database := openPostgresTestSchema(t)
	before := fstest.MapFS{}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "0060_") {
			continue
		}
		name := "migrations/" + entry.Name()
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: data}
	}
	if err := applyMigrations(database, before); err != nil {
		t.Fatal(err)
	}
	const insert = `INSERT INTO storage_assets(id,name,kind,provider,claim_name,root_prefix,read_only,browse_enabled) VALUES (?,?, 'dataset',?, 'approved-data',?,TRUE,?)`
	if err := database.Exec(insert, "legacy", "legacy", "idc", "", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(insert, "early", "early", "pvc", "", false).Error; err == nil {
		t.Fatal("old schema unexpectedly accepted pvc")
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Exec(insert, "portable", "portable", "pvc", "", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(insert, "bad-root", "bad", "pvc", "private/", false).Error; err == nil {
		t.Fatal("PVC object-store root accepted")
	}
	if err := database.Exec(insert, "bad-browse", "bad", "pvc", "", true).Error; err == nil {
		t.Fatal("PVC browser without a backend accepted")
	}
	var count int64
	if err := database.Table("storage_assets").Where("id IN ?", []string{"legacy", "portable"}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("migration lost assets: count=%d err=%v", count, err)
	}
}
