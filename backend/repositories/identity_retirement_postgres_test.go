package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

// Run with POSTGRES_TEST_DSN to verify separate connections cannot issue
// credentials across the retirement commit boundary.
func TestIdentityWritersWaitForTenantRetirementPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("identity_retirement_test_%d", time.Now().UnixNano())
	quoted := `"` + schema + `"`
	if err := admin.Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	first := openArtifactPostgresConnection(t, dsn)
	second := openArtifactPostgresConnection(t, dsn)
	for _, database := range []*gorm.DB{first, second} {
		if err := database.Exec("SET search_path TO " + quoted).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := first.AutoMigrate(&TenantRecord{}, &UserRecord{}, &LocalUserRecord{}, &LocalSessionRecord{}, &PersonalAccessTokenRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"identity", "user", "session", "PAT"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			repo := NewGormRepository(second)
			principal := auth.Principal{Subject: operation, TenantID: operation, Username: operation, Roles: []string{"Engineer"}}
			if err := repo.EnsureIdentity(ctx, principal); err != nil {
				t.Fatal(err)
			}
			tx := first.WithContext(ctx).Begin()
			defer tx.Rollback()
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 34781))", operation).Error; err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				switch operation {
				case "identity":
					result <- repo.EnsureIdentity(ctx, principal)
				case "user":
					result <- repo.CreateLocalUser(ctx, domain.LocalUser{ID: operation, Username: operation, TenantID: operation, Roles: []string{"Engineer"}, PasswordHash: "test-hash"})
				case "session":
					result <- repo.CreateLocalSession(ctx, domain.LocalSession{ID: operation, PublicID: operation, UserID: operation, TenantID: operation}, "digest")
				case "PAT":
					result <- repo.CreatePersonalAccessToken(ctx, domain.PersonalAccessToken{ID: operation, PublicID: operation, UserID: operation, TenantID: operation, Scopes: []string{"jobs:read"}}, strings.Repeat("a", 64))
				}
			}()
			select {
			case err := <-result:
				t.Fatalf("writer crossed retirement fence: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if err := tx.Model(&TenantRecord{}).Where("id = ?", operation).Updates(map[string]any{"retired_at": time.Now(), "retired_by": "admin"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrTenantRetirementBlocked) {
					t.Fatalf("writer must reject committed retirement, got %v", err)
				}
			case <-ctx.Done():
				t.Fatal("identity writer did not finish after retirement")
			}
		})
	}
}
