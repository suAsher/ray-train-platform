package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func identityMembershipRepository(t *testing.T) *GormRepository {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-identity-membership?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &UserRecord{}, &LocalUserRecord{}, &TenantMembershipRecord{}, &PersonalAccessTokenRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"team-a", "team-b"} {
		if err := database.Create(&TenantRecord{ID: id, Name: id, Namespace: "tenant-" + id, LocalQueue: id + "-gpu", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repository := NewGormRepository(database)
	if err := repository.EnsureIdentity(context.Background(), auth.Principal{
		Subject: "user-a", Username: "alice", Email: "alice@old.example", TenantID: "team-a", Roles: []string{domain.RoleEngineer},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateLocalUser(context.Background(), domain.LocalUser{
		ID: "user-a", Username: "alice", StorageKey: "stable-alice", Email: "alice@old.example",
		TenantID: "team-a", Roles: []string{domain.RoleEngineer}, PasswordHash: "!external-only",
		IdentityProvider: domain.IdentityProviderOAuth2Proxy,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutTenantMembership(context.Background(), domain.TenantMembership{
		IdentityID: "user-a", TenantID: "team-b", Roles: []string{domain.RoleTenantAdmin}, Status: domain.MembershipStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestEnsureIdentityPreservesStorageHomeAcrossTeamSwitch(t *testing.T) {
	repository := identityMembershipRepository(t)
	if err := repository.SetActiveTenant(context.Background(), "user-a", "team-b"); err != nil {
		t.Fatalf("switch active team: %v", err)
	}
	if err := repository.EnsureIdentity(context.Background(), auth.Principal{
		Subject: "user-a", Username: "alice", Email: "alice@new.example", TenantID: "team-b", Roles: []string{domain.RoleTenantAdmin},
	}); err != nil {
		t.Fatalf("persist switched identity: %v", err)
	}
	var legacy UserRecord
	if err := repository.db.Where("id = ?", "user-a").First(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	var roles []string
	if err := json.Unmarshal([]byte(legacy.RolesJSON), &roles); err != nil {
		t.Fatal(err)
	}
	if legacy.TenantID != "team-a" || len(roles) != 1 || roles[0] != domain.RoleEngineer {
		t.Fatalf("legacy storage home or roles drifted with active team: tenant=%q roles=%v", legacy.TenantID, roles)
	}
	if legacy.Email != "alice@new.example" {
		t.Fatalf("global identity metadata was not refreshed: %+v", legacy)
	}
}

func TestListUserSummariesUsesActiveMembership(t *testing.T) {
	repository := identityMembershipRepository(t)
	if err := repository.SetActiveTenant(context.Background(), "user-a", "team-b"); err != nil {
		t.Fatalf("switch active team: %v", err)
	}
	items, err := repository.ListUserSummaries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TenantID != "team-b" || len(items[0].Roles) != 1 || items[0].Roles[0] != domain.RoleTenantAdmin {
		t.Fatalf("admin summary did not use active membership: %+v", items)
	}
}

