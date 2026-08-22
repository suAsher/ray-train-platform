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

func TestDecommissionedLocalUserDisappearsFromAccountManagement(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&LocalUserRecord{}, &JobRecord{}, &WorkspaceRecord{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	repo := NewGormRepository(database)
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	if err := database.Create(&LocalUserRecord{
		ID: "user-delete", Username: "delete.me", StorageKey: "delete.me", TenantID: "local",
		RolesJSON: `["Engineer"]`, PasswordHash: "unused", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if err := repo.DecommissionLocalUser(context.Background(), "user-delete", now.Add(time.Hour)); err != nil {
		t.Fatalf("decommission user: %v", err)
	}
	users, err := repo.ListLocalUsers(context.Background())
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("decommissioned account must be hidden from account management, got %#v", users)
	}
	if _, err := repo.FindLocalUserByUsername(context.Background(), "delete.me"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatalf("decommissioned account must not be usable for login, got %v", err)
	}
	var retained LocalUserRecord
	if err := database.First(&retained, "id = ?", "user-delete").Error; err != nil {
		t.Fatalf("audit row must be retained: %v", err)
	}
	if !retained.Disabled || retained.DecommissionedAt == nil {
		t.Fatalf("decommission must disable and timestamp the retained row: %#v", retained)
	}
}
