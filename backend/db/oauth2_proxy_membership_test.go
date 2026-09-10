package db

import (
	"strings"
	"testing"
)

func TestOAuth2ProxyMembershipMigrationMarksIdentityProvider(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0040_oauth2_proxy_membership.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.Join(strings.Fields(string(contents)), " ")
	for _, fragment := range []string{
		"SET LOCAL lock_timeout = '5s';",
		"SET LOCAL statement_timeout = '60s';",
		"ADD COLUMN IF NOT EXISTS identity_provider TEXT NOT NULL DEFAULT 'local'",
		"CHECK (identity_provider IN ('local', 'oauth2-proxy'))",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
}
