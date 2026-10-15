package db

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPersonalStorageMigrationIdentityAndPublicMetadata(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0059_personal_storage.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(strings.Fields(string(contents)), " ")
	for _, fragment := range []string{"SET LOCAL lock_timeout = '5s';", "SET LOCAL statement_timeout = '60s';", "owner_id TEXT PRIMARY KEY REFERENCES users(id)", "UNIQUE (username)", "CHECK (generation = 1)", "state IN ('PENDING', 'READY')"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"tenant_id", "private_key", "password"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("personal metadata migration contains %s", forbidden)
		}
	}
}
func TestPersonalStoragePostgresMigrationUpgradeAndIsolation(t *testing.T) {
	database := openPostgresTestSchema(t)
	before := fstest.MapFS{}
	versions, err := migrationVersions(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range versions {
		if version >= 59 {
			continue
		}
		entries, err := migrationFiles.ReadDir("migrations")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), fmt.Sprintf("%04d_", version)) {
				data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				before["migrations/"+entry.Name()] = &fstest.MapFile{Data: data}
			}
		}
	}
	if err := applyMigrations(database, before); err != nil {
		t.Fatal(err)
	}
	seedPostgresIdentityRows(t, database)
	if err := ApplyMigrations(database); err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO wellspiking_personal_bindings(owner_id,id,username,secret_name,public_key,fingerprint,generation,state) VALUES ('user-a1','binding-a','user-a1','secret-a','public-a','fingerprint-a',1,'PENDING')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO wellspiking_personal_bindings(owner_id,id,username,secret_name,public_key,fingerprint,generation,state) VALUES ('user-b1','binding-b','user-a1','secret-b','public-b','fingerprint-b',1,'PENDING')`).Error; err == nil {
		t.Fatal("duplicate upstream username accepted")
	}
	if err := database.Exec(`UPDATE users SET tenant_id='tenant-b' WHERE id='user-a1'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`UPDATE wellspiking_personal_bindings SET username='changed' WHERE owner_id='user-a1'`).Error; err == nil {
		t.Fatal("immutable upstream account changed")
	}
	if err := ApplyMigrations(database); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.Table("wellspiking_personal_bindings").Where("owner_id = ?", "user-a1").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("binding lost %d %v", count, err)
	}
}
