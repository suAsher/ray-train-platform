package db

import (
	"strings"
	"testing"
)

func TestRetiredLocalUserCleanupMigrationIsNarrow(t *testing.T) {
	sql, err := migrationFiles.ReadFile("migrations/0035_retired_local_user_cleanup.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"NEW.disabled IS TRUE", "OLD.decommissioned_at IS NULL", "NEW.decommissioned_at IS NOT NULL", "to_jsonb(NEW) - ARRAY['disabled','decommissioned_at','updated_at']", "OLD.tenant_id", "NEW.tenant_id", "pg_advisory_xact_lock_shared"} {
		if !strings.Contains(string(sql), fragment) {
			t.Errorf("missing safety boundary %s", fragment)
		}
	}
}

func TestPostgresRetiredLocalUserCleanupOnlyAllowsMonotonicDecommission(t *testing.T) {
	database := openPostgresTestSchema(t)
	if err := ApplyMigrations(database); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO tenants(id,name,namespace,local_queue) VALUES ('old','old','old','old'),('live','live','live','live')`,
		`INSERT INTO local_users(id,username,tenant_id,roles,password_hash) VALUES ('old-user','old.user','old','["Engineer"]','original'),('live-user','live.user','live','["Engineer"]','original')`,
		`UPDATE tenants SET retired_at=NOW(),retired_by='admin' WHERE id='old'`,
	} {
		if err := database.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		`UPDATE local_users SET disabled=TRUE WHERE id='old-user'`,
		`UPDATE local_users SET disabled=TRUE,decommissioned_at=NOW(),password_hash='changed' WHERE id='old-user'`,
		`UPDATE local_users SET disabled=TRUE,decommissioned_at=NOW(),tenant_id='live' WHERE id='old-user'`,
		`UPDATE local_users SET disabled=TRUE,decommissioned_at=NOW(),roles='["SuperAdmin"]' WHERE id='old-user'`,
		`DELETE FROM local_users WHERE id='old-user'`,
	} {
		if err := database.Exec(sql).Error; err == nil {
			t.Fatalf("accepted unsafe mutation: %s", sql)
		}
	}
	if err := database.Exec(`UPDATE local_users SET disabled=TRUE,decommissioned_at=NOW(),updated_at=NOW() WHERE id='old-user'`).Error; err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE local_users SET disabled=FALSE WHERE id='old-user'`,
		`UPDATE local_users SET decommissioned_at=NULL WHERE id='old-user'`,
		`UPDATE local_users SET password_hash='changed' WHERE id='old-user'`,
		`UPDATE local_users SET decommissioned_at=NOW() WHERE id='old-user'`,
	} {
		if err := database.Exec(sql).Error; err == nil {
			t.Fatalf("accepted resurrection: %s", sql)
		}
	}
	if err := database.Exec(`UPDATE local_users SET password_hash='changed' WHERE id='live-user'`).Error; err != nil {
		t.Fatal(err)
	}
}
