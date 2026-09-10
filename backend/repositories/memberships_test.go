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

func membershipRepository(t *testing.T) *GormRepository {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &LocalUserRecord{}, &TenantMembershipRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"team-a", "team-b"} {
		if err := database.Create(&TenantRecord{ID: id, Name: id, Namespace: "tenant-" + id, LocalQueue: id + "-gpu", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Create(&LocalUserRecord{ID: "user-a", Username: "alice", StorageKey: "stable-alice", TenantID: "team-a", ActiveTenantID: "team-a", RolesJSON: `["Engineer"]`, GlobalRolesJSON: `[]`, PasswordHash: "!", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	for _, membership := range []domain.TenantMembership{
		{IdentityID: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, Status: domain.MembershipStatusActive},
		{IdentityID: "user-a", TenantID: "team-b", Roles: []string{domain.RoleTenantAdmin}, Status: domain.MembershipStatusActive},
	} {
		if err := repository.PutTenantMembership(context.Background(), membership); err != nil {
			t.Fatal(err)
		}
	}
	return repository
}

func TestMembershipSwitchChangesAuthorizationWithoutChangingStorageIdentity(t *testing.T) {
	repository := membershipRepository(t)
	if err := repository.SetActiveTenant(context.Background(), "user-a", "team-b"); err != nil {
		t.Fatalf("switch team: %v", err)
	}
	user, found, err := repository.ResolveOAuth2ProxyAccount(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("resolve switched account: found=%t err=%v", found, err)
	}
	if user.TenantID != "team-b" || user.StorageKey != "stable-alice" || len(user.Roles) != 1 || user.Roles[0] != domain.RoleTenantAdmin {
		t.Fatalf("unexpected switched account: %+v", user)
	}
	memberships, err := repository.ListTenantMemberships(context.Background(), "user-a")
	if err != nil || len(memberships) != 2 || memberships[0].Active || !memberships[1].Active {
		t.Fatalf("unexpected memberships: %+v err=%v", memberships, err)
	}
}

func TestMembershipSwitchAndDisableFailClosed(t *testing.T) {
	repository := membershipRepository(t)
	if err := repository.SetActiveTenant(context.Background(), "user-a", "missing"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("missing membership error=%v", err)
	}
	if err := repository.SetTenantMembershipStatus(context.Background(), "user-a", "team-a", domain.MembershipStatusInactive); !errors.Is(err, ErrLastMembership) {
		t.Fatalf("active membership disable error=%v", err)
	}
	if err := repository.SetTenantMembershipStatus(context.Background(), "user-a", "team-b", domain.MembershipStatusInactive); err != nil {
		t.Fatalf("disable secondary membership: %v", err)
	}
	if err := repository.SetActiveTenant(context.Background(), "user-a", "team-b"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("inactive membership switch error=%v", err)
	}
}
