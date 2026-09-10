package db

import (
	"strings"
	"testing"
)

func TestTenantMembershipMigrationBackfillsActiveMemberships(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0043_tenant_memberships.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.Join(strings.Fields(string(contents)), " ")
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS active_tenant_id TEXT REFERENCES tenants(id)",
		"ADD COLUMN IF NOT EXISTS global_roles JSONB NOT NULL DEFAULT '[]'::jsonb",
		"CREATE TABLE IF NOT EXISTS tenant_memberships",
		"PRIMARY KEY (identity_id, tenant_id)",
		"CHECK (status IN ('ACTIVE', 'INACTIVE'))",
		"INSERT INTO tenant_memberships(identity_id, tenant_id, roles, status, created_at, updated_at)",
		"ON CONFLICT (identity_id, tenant_id) DO NOTHING",
		"tenant_memberships_lifecycle_write_guard",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
}
