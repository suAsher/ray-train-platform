package db

import (
	"strings"
	"testing"
)

func TestImageDefaultUniquenessMigrationScopesTenantAndSharedDefaults(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0018_image_default_uniqueness.up.sql")
	if err != nil {
		t.Fatalf("read image-default migration: %v", err)
	}
	sql := string(contents)
	for _, fragment := range []string{
		"UNIQUE INDEX IF NOT EXISTS platform_images_tenant_default_uidx",
		"ON platform_images(tenant_id, kind)",
		"WHERE tenant_id IS NOT NULL AND is_default = TRUE",
		"UNIQUE INDEX IF NOT EXISTS platform_images_shared_default_uidx",
		"ON platform_images(kind)",
		"WHERE tenant_id IS NULL AND is_default = TRUE",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
}
