package repositories

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
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

func TestProvisionOAuth2ProxyAccountCreatesOnlyEngineerExternalMember(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &LocalUserRecord{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Now().UTC()
	if err := database.Create(&TenantRecord{ID: "local", Name: "Local", Namespace: "tenant-local", LocalQueue: "local-gpu", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	repository := NewGormRepository(database)
	created, err := repository.ProvisionOAuth2ProxyAccount(context.Background(), " Alice ", "alice@example.com", "local")
	if err != nil {
		t.Fatalf("provision account: %v", err)
	}
	if created.Username != "alice" || created.StorageKey != "alice" || created.TenantID != "local" || created.Email != "alice@example.com" ||
		len(created.Roles) != 1 || created.Roles[0] != domain.RoleEngineer || created.IdentityProvider != domain.IdentityProviderOAuth2Proxy {
		t.Fatalf("unexpected account: %#v", created)
	}
	if created.PasswordHash == "" || domain.VerifyPassword(created.PasswordHash, "anything") {
		t.Fatal("external member must not have a usable local password")
	}

	again, err := repository.ProvisionOAuth2ProxyAccount(context.Background(), "alice", "changed@example.com", "local")
	if err != nil || again.ID != created.ID || again.Roles[0] != domain.RoleEngineer {
		t.Fatalf("concurrent/idempotent provision changed membership: account=%#v err=%v", again, err)
	}
	var count int64
	if err := database.Model(&LocalUserRecord{}).Where("username = ?", "alice").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("account count=%d err=%v", count, err)
	}
}

func TestProvisionOAuth2ProxyAccountRejectsMissingTenantAndRetiredIdentity(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &LocalUserRecord{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	repository := NewGormRepository(database)
	if _, err := repository.ProvisionOAuth2ProxyAccount(context.Background(), "alice", "", "missing"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("missing tenant error=%v", err)
	}
	now := time.Now().UTC()
	if err := database.Create(&TenantRecord{ID: "local", Name: "Local", Namespace: "tenant-local", LocalQueue: "local-gpu", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := database.Create(&LocalUserRecord{ID: "retired", Username: "alice", StorageKey: "alice", TenantID: "local", RolesJSON: `["Engineer"]`, PasswordHash: "!external-only", IdentityProvider: domain.IdentityProviderOAuth2Proxy, Disabled: true, DecommissionedAt: &now}).Error; err != nil {
		t.Fatalf("seed retired account: %v", err)
	}
	if _, err := repository.ProvisionOAuth2ProxyAccount(context.Background(), "alice", "", "local"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("retired identity error=%v", err)
	}
}
