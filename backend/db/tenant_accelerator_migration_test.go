package db

import (
	"strings"
	"testing"
)

func TestTenantAcceleratorMigrationUsesClosedSupportedSet(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0044_tenant_accelerator_class.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(strings.Fields(string(contents)), " ")
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS accelerator_class TEXT NOT NULL DEFAULT 'rtx4090'",
		"CHECK (accelerator_class IN ('rtx4090', 'a100', 'a800', 'h20'))",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
}
