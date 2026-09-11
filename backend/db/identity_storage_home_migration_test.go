package db

import (
	"strings"
	"testing"
)

func TestIdentityStorageHomeMigrationPreservesHistoryWithoutGrantingMembership(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0045_identity_storage_home.up.sql")
	if err != nil {
		t.Fatalf("read identity storage-home migration: %v", err)
	}
	sql := strings.Join(strings.Fields(string(contents)), " ")
	for _, fragment := range []string{
		"SET LOCAL lock_timeout = '5s';",
		"SET LOCAL statement_timeout = '60s';",
		"ADD COLUMN IF NOT EXISTS storage_tenant_id TEXT REFERENCES tenants(id)",
		"CREATE TABLE IF NOT EXISTS identity_tenant_ownerships",
		"PRIMARY KEY (identity_id, tenant_id)",
		"INSERT INTO identity_tenant_ownerships",
		"FROM tenant_memberships",
		"FROM source_artifacts",
		"REFERENCES identity_tenant_ownerships(identity_id, tenant_id) ON DELETE RESTRICT",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 45 missing %q", fragment)
		}
	}
	if strings.Contains(sql, "INSERT INTO tenant_memberships") {
		t.Fatal("historical ownership migration must not grant a tenant membership")
	}
	if strings.Contains(sql, "DELETE FROM") {
		t.Fatal("historical ownership migration must not delete user data")
	}
}

