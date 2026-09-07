package repositories

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRetiredLocalUserCleanupPreservesIdentityAndRejectsActiveTeam(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &LocalUserRecord{}, &JobRecord{}, &WorkspaceRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, tenant := range []TenantRecord{{ID: "old", Namespace: "old", RetiredAt: &now}, {ID: "live", Namespace: "live"}} {
		if err := database.Create(&tenant).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&LocalUserRecord{ID: tenant.ID + "-user", Username: tenant.ID + ".user", TenantID: tenant.ID, RolesJSON: `["Engineer"]`, PasswordHash: "original", StorageKey: "keep-files"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := NewGormRepository(database)
	ctx := context.Background()
	// Login remains closed even before account cleanup.
	if _, err := repo.FindLocalUserByID(ctx, "old-user"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("login lookup=%v", err)
	}
	if _, err := repo.FindRetiredLocalUserByID(ctx, "old-user"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DecommissionRetiredLocalUser(ctx, "live-user", now); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("active team cleanup=%v", err)
	}
	if err := database.Create(&JobRecord{ID: "history", UserID: "old-user", TenantID: "old", ObservedState: "RUNNING"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.DecommissionRetiredLocalUser(ctx, "old-user", now); !errors.Is(err, ErrLocalUserActiveWorkloads) {
		t.Fatalf("active job cleanup=%v", err)
	}
	if err := database.Model(&JobRecord{}).Where("id = ?", "history").Update("observed_state", "SUCCEEDED").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.DecommissionRetiredLocalUser(ctx, "old-user", now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindRetiredLocalUserByID(ctx, "old-user"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("removed account still visible: %v", err)
	}
	var row LocalUserRecord
	if err := database.First(&row, "id = ?", "old-user").Error; err != nil {
		t.Fatal(err)
	}
	if !row.Disabled || row.DecommissionedAt == nil || row.PasswordHash != "original" || row.StorageKey != "keep-files" || row.TenantID != "old" {
		t.Fatalf("unsafe cleanup: %+v", row)
	}
	var history JobRecord
	if err := database.First(&history, "id = ?", "history").Error; err != nil {
		t.Fatal(err)
	}
	if history.ObservedState != "SUCCEEDED" || history.UserID != "old-user" {
		t.Fatalf("history changed: %+v", history)
	}
}

func TestRetiredLocalUserCleanupRechecksLockedRowsAndDatabaseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, table, mutation string
		queryErr              error
		wantMissing           bool
	}{
		{name: "tenant removed", table: "tenants", mutation: `DELETE FROM tenants WHERE id='old'`, wantMissing: true},
		{name: "tenant no longer retired", table: "tenants", mutation: `UPDATE tenants SET retired_at=NULL WHERE id='old'`, wantMissing: true},
		{name: "tenant query failed", table: "tenants", queryErr: errors.New("tenant query failed")},
		{name: "user moved", table: "local_users", mutation: `UPDATE local_users SET tenant_id='live' WHERE id='old-user'`, wantMissing: true},
		{name: "user already removed", table: "local_users", mutation: `UPDATE local_users SET decommissioned_at=CURRENT_TIMESTAMP WHERE id='old-user'`, wantMissing: true},
		{name: "user query failed", table: "local_users", queryErr: errors.New("user query failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.AutoMigrate(&TenantRecord{}, &LocalUserRecord{}, &JobRecord{}, &WorkspaceRecord{}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err := database.Create(&TenantRecord{ID: "old", Namespace: "old", RetiredAt: &now}).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Create(&LocalUserRecord{ID: "old-user", Username: "old.user", TenantID: "old", RolesJSON: `["Engineer"]`, PasswordHash: "original"}).Error; err != nil {
				t.Fatal(err)
			}
			injected := false
			if err := database.Callback().Query().Before("gorm:query").Register("test:retired-row-change", func(tx *gorm.DB) {
				if injected || tx.Statement.Table != tc.table || len(tx.Statement.Joins) > 0 {
					return
				}
				injected = true
				if tc.queryErr != nil {
					tx.AddError(tc.queryErr)
					return
				}
				if err := tx.Session(&gorm.Session{NewDB: true}).Exec(tc.mutation).Error; err != nil {
					tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err = NewGormRepository(database).DecommissionRetiredLocalUser(context.Background(), "old-user", now)
			if !injected || err == nil {
				t.Fatalf("did not reject changed row: injected=%v err=%v", injected, err)
			}
			if tc.wantMissing && !errors.Is(err, ErrLocalUserNotFound) {
				t.Fatalf("want not found, got %v", err)
			}
			if tc.queryErr != nil && !errors.Is(err, tc.queryErr) {
				t.Fatalf("want database failure, got %v", err)
			}
			var retained LocalUserRecord
			if err := database.First(&retained, "id = ?", "old-user").Error; err != nil {
				t.Fatal(err)
			}
			if retained.Disabled || retained.DecommissionedAt != nil {
				t.Fatal("failure partially decommissioned user")
			}
		})
	}
}

func TestRetiredLocalUserLookupFailsClosedOnMissingSchema(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewGormRepository(database)
	if _, err := repo.FindRetiredLocalUserByID(context.Background(), "missing"); err == nil || errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("query failure masked: %v", err)
	}
	if err := repo.DecommissionRetiredLocalUser(context.Background(), "missing", time.Now()); err == nil {
		t.Fatal("cleanup accepted missing schema")
	}
}
