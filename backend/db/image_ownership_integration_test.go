package db

import "testing"

func TestPostgresImageOwnershipUpgradePreservesBaseAndChecksPrivateScope(t *testing.T) {
	database := openPostgresTestSchema(t)
	applyPostgresMigrationsThrough(t, database, 54)
	for _, sql := range []string{
		`INSERT INTO tenants(id,name,namespace,local_queue) VALUES('image-team','Image team','image-team','image-team')`,
		`INSERT INTO platform_images(id,name,reference,kind,is_default,created_by) VALUES('base','Original base','registry.example/base:v1','training',TRUE,'historical-admin')`,
	} {
		if err := database.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(database); err != nil {
			t.Fatal(err)
		}
	}
	var preserved int64
	if err := database.Raw(`SELECT COUNT(*) FROM platform_images WHERE id='base' AND tenant_id IS NULL AND is_default=TRUE AND owner_user_id='' AND visibility='' AND created_by='historical-admin'`).Scan(&preserved).Error; err != nil || preserved != 1 {
		t.Fatalf("base image changed: count=%d err=%v", preserved, err)
	}
	for _, tc := range []struct {
		visibility, owner string
		tenant            any
		def, allowed      bool
	}{
		{"personal", "owner", "image-team", false, true},
		{"personal", "owner", nil, false, false},
		{"personal", "", "image-team", false, false},
		{"personal", "owner", "image-team", true, false},
		{"team", "owner", "image-team", false, true},
		{"invalid", "owner", "image-team", false, false},
	} {
		err := database.Exec(`INSERT INTO platform_images(id,name,reference,kind,tenant_id,owner_user_id,visibility,is_default) VALUES('candidate','Candidate','registry.example/owned:v1','training',?,?,?,?)`, tc.tenant, tc.owner, tc.visibility, tc.def).Error
		if (err == nil) != tc.allowed {
			t.Fatalf("constraint %+v: %v", tc, err)
		}
		if err == nil {
			if err = database.Exec(`DELETE FROM platform_images WHERE id='candidate'`).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
}
