package repositories

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestResolveOAuth2ProxyAccountUsesActivePlatformMembership(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &LocalUserRecord{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	if err := database.Create(&TenantRecord{
		ID: "local", Name: "Local", Namespace: "tenant-local", LocalQueue: "local-gpu",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := database.Create(&LocalUserRecord{
		ID: "platform-user-1", Username: "guofeng.su", StorageKey: "guofeng.su", TenantID: "local",
		RolesJSON: `["SuperAdmin"]`, PasswordHash: "unused", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	repository := NewGormRepository(database)
	user, found, err := repository.ResolveOAuth2ProxyAccount(context.Background(), " GUOFENG.SU ")
	if err != nil || !found {
		t.Fatalf("resolve account: found=%t err=%v", found, err)
	}
	if user.ID != "platform-user-1" || user.TenantID != "local" || len(user.Roles) != 1 || user.Roles[0] != "SuperAdmin" {
		t.Fatalf("unexpected platform membership: %#v", user)
	}

	_, found, err = repository.ResolveOAuth2ProxyAccount(context.Background(), "missing.user")
	if err != nil || found {
		t.Fatalf("missing account: found=%t err=%v", found, err)
	}
}
