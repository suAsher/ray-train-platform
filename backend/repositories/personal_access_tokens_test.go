package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func patTestRepository(t *testing.T) *GormRepository {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-pat?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PAT database: %v", err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &UserRecord{}, &PersonalAccessTokenRecord{}); err != nil {
		t.Fatalf("migrate PAT database: %v", err)
	}
	return NewGormRepository(database)
}

func ensurePATIdentity(t *testing.T, repo *GormRepository, tenantID, userID string) {
	t.Helper()
	err := repo.EnsureIdentity(context.Background(), auth.Principal{
		Subject: userID, Username: userID, Email: userID + "@example.com", TenantID: tenantID, Roles: []string{"Engineer"},
	})
	if err != nil {
		t.Fatalf("ensure PAT identity: %v", err)
	}
}

func issueRepositoryPAT(t *testing.T, repo *GormRepository, tenantID, userID string, now time.Time) domain.IssuedPersonalAccessToken {
	t.Helper()
	issued, err := domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{
		ID: "pat-" + tenantID + "-" + userID, TenantID: tenantID, UserID: userID,
		Scopes: []string{domain.PATScopeSourcesWrite, domain.PATScopeJobsRead},
	}, []byte("0123456789abcdef0123456789abcdef"), now)
	if err != nil {
		t.Fatalf("issue PAT: %v", err)
	}
	if err := repo.CreatePersonalAccessToken(context.Background(), issued.PersonalAccessToken, issued.Digest); err != nil {
		t.Fatalf("create PAT: %v", err)
	}
	return issued
}

func TestPersonalAccessTokenRepositoryClassifiesLookupErrors(t *testing.T) {
	repo := patTestRepository(t)
	if _, err := repo.FindPATByPublicID(context.Background(), "missing-public-id"); !errors.Is(err, auth.ErrPATNotFound) {
		t.Fatalf("missing record must map to auth not-found sentinel, got %v", err)
	}
	sqlDB, err := repo.db.DB()
	if err != nil {
		t.Fatalf("get test SQL database: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close test SQL database: %v", err)
	}
	if _, err := repo.FindPATByPublicID(context.Background(), "missing-public-id"); err == nil || errors.Is(err, auth.ErrPATNotFound) {
		t.Fatalf("database failure must remain distinct from not found, got %v", err)
	}
}

func TestPersonalAccessTokenRepositoryClassifiesOwnerLookupErrors(t *testing.T) {
	t.Run("missing owner maps to auth not found", func(t *testing.T) {
		repo := patTestRepository(t)
		ensurePATIdentity(t, repo, "tenant-a", "user-a")
		issued := issueRepositoryPAT(t, repo, "tenant-a", "user-a", time.Now().UTC())
		if err := repo.db.Where("id = ? AND tenant_id = ?", "user-a", "tenant-a").Delete(&UserRecord{}).Error; err != nil {
			t.Fatalf("delete PAT owner: %v", err)
		}
		if _, err := repo.FindPATByPublicID(context.Background(), issued.PublicID); !errors.Is(err, auth.ErrPATNotFound) {
			t.Fatalf("missing owner must map to auth not-found sentinel, got %v", err)
		}
	})

	t.Run("owner query failure remains internal", func(t *testing.T) {
		repo := patTestRepository(t)
		ensurePATIdentity(t, repo, "tenant-a", "user-a")
		issued := issueRepositoryPAT(t, repo, "tenant-a", "user-a", time.Now().UTC())
		ownerFailure := errors.New("owner database unavailable")
		if err := repo.db.Callback().Query().Before("gorm:query").Register("test:fail-pat-owner-query", func(tx *gorm.DB) {
			if tx.Statement.Table == "users" {
				tx.AddError(ownerFailure)
			}
		}); err != nil {
			t.Fatalf("register owner query failure: %v", err)
		}
		_, err := repo.FindPATByPublicID(context.Background(), issued.PublicID)
		if !errors.Is(err, ownerFailure) || errors.Is(err, auth.ErrPATNotFound) {
			t.Fatalf("owner database failure must remain internal, got %v", err)
		}
	})
}

func TestPersonalAccessTokenRepositoryCreatesListsMetadataAndRestoresPrincipal(t *testing.T) {
	repo := patTestRepository(t)
	ensurePATIdentity(t, repo, "tenant-a", "user-a")
	issued := issueRepositoryPAT(t, repo, "tenant-a", "user-a", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))

	items, err := repo.ListPersonalAccessTokens(context.Background(), "tenant-a", "user-a")
	if err != nil {
		t.Fatalf("list PATs: %v", err)
	}
	if len(items) != 1 || items[0].PublicID != issued.PublicID || strings.Join(items[0].Scopes, ",") != "jobs:read,sources:write" {
		t.Fatalf("unexpected PAT metadata: %+v", items)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal PAT metadata: %v", err)
	}
	if strings.Contains(string(encoded), issued.Digest) || strings.Contains(string(encoded), issued.Token) || strings.Contains(string(encoded), "token_digest") {
		t.Fatal("PAT digest or plaintext leaked in metadata JSON")
	}

	record, err := repo.FindPATByPublicID(context.Background(), issued.PublicID)
	if err != nil {
		t.Fatalf("find PAT by public id: %v", err)
	}
	if record.Digest != issued.Digest || record.Principal.Subject != "user-a" || record.Principal.TenantID != "tenant-a" || !record.Principal.HasRole("Engineer") {
		t.Fatalf("unexpected authentication record public=%q subject=%q tenant=%q engineer=%t", record.PublicID, record.Principal.Subject, record.Principal.TenantID, record.Principal.HasRole("Engineer"))
	}
}

func TestTeamBoundPATStillUsesItsMembershipAfterInteractiveTeamSwitch(t *testing.T) {
	repo := patTestRepository(t)
	if err := repo.db.AutoMigrate(&LocalUserRecord{}, &TenantMembershipRecord{}); err != nil {
		t.Fatalf("migrate membership tables: %v", err)
	}
	ensurePATIdentity(t, repo, "team-a", "user-a")
	ensurePATIdentity(t, repo, "team-b", "seed-b")
	if err := repo.CreateLocalUser(context.Background(), domain.LocalUser{
		ID: "user-a", Username: "user-a", StorageKey: "stable-user-a", Email: "user-a@example.com",
		TenantID: "team-a", Roles: []string{domain.RoleEngineer}, PasswordHash: "!external-only",
		IdentityProvider: domain.IdentityProviderOAuth2Proxy,
	}); err != nil {
		t.Fatalf("create local account: %v", err)
	}
	if err := repo.PutTenantMembership(context.Background(), domain.TenantMembership{
		IdentityID: "user-a", TenantID: "team-b", Roles: []string{domain.RoleEngineer}, Status: domain.MembershipStatusActive,
	}); err != nil {
		t.Fatalf("add second membership: %v", err)
	}
	issued := issueRepositoryPAT(t, repo, "team-a", "user-a", time.Now().UTC())
	if err := repo.SetActiveTenant(context.Background(), "user-a", "team-b"); err != nil {
		t.Fatalf("switch interactive team: %v", err)
	}
	record, err := repo.FindPATByPublicID(context.Background(), issued.PublicID)
	if err != nil {
		t.Fatalf("authenticate team-bound PAT: %v", err)
	}
	if record.Principal.TenantID != "team-a" || record.Principal.Username != "user-a" || !record.Principal.HasRole(domain.RoleEngineer) {
		t.Fatalf("PAT principal drifted with interactive team: %+v", record.Principal)
	}
	if err := repo.SetTenantMembershipStatus(context.Background(), "user-a", "team-a", domain.MembershipStatusInactive); err != nil {
		t.Fatalf("disable PAT membership: %v", err)
	}
	if _, err := repo.FindPATByPublicID(context.Background(), issued.PublicID); !errors.Is(err, auth.ErrPATNotFound) {
		t.Fatalf("inactive membership must invalidate PAT, got %v", err)
	}
}

func TestPersonalAccessTokenRepositoryEnforcesOwnerIsolationOnListAndRevoke(t *testing.T) {
	repo := patTestRepository(t)
	ensurePATIdentity(t, repo, "tenant-a", "user-a")
	ensurePATIdentity(t, repo, "tenant-b", "user-b")
	issued := issueRepositoryPAT(t, repo, "tenant-a", "user-a", time.Now().UTC())

	items, err := repo.ListPersonalAccessTokens(context.Background(), "tenant-b", "user-b")
	if err != nil || len(items) != 0 {
		t.Fatalf("other owner must not see PAT, items=%+v err=%v", items, err)
	}
	if err := repo.RevokePersonalAccessToken(context.Background(), "tenant-b", "user-b", issued.ID, time.Now().UTC()); !errors.Is(err, ErrPersonalAccessTokenNotFound) {
		t.Fatalf("cross-owner revoke must look not found, got %v", err)
	}
	record, err := repo.FindPATByPublicID(context.Background(), issued.PublicID)
	if err != nil || record.RevokedAt != nil {
		t.Fatalf("cross-owner revoke changed token public=%q revoked=%t err=%v", record.PublicID, record.RevokedAt != nil, err)
	}

	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.RevokePersonalAccessToken(context.Background(), "tenant-a", "user-a", issued.ID, revokedAt); err != nil {
		t.Fatalf("owner revoke PAT: %v", err)
	}
	record, err = repo.FindPATByPublicID(context.Background(), issued.PublicID)
	if err != nil || record.RevokedAt == nil || !record.RevokedAt.Equal(revokedAt) {
		t.Fatalf("expected revoked authentication record public=%q revoked=%t err=%v", record.PublicID, record.RevokedAt != nil, err)
	}
}

func TestPersonalAccessTokenRepositoryThrottlesLastUsedUpdatesForFiveMinutes(t *testing.T) {
	repo := patTestRepository(t)
	ensurePATIdentity(t, repo, "tenant-a", "user-a")
	issued := issueRepositoryPAT(t, repo, "tenant-a", "user-a", time.Now().UTC())
	first := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := repo.TouchPATLastUsed(context.Background(), issued.PublicID, first); err != nil {
		t.Fatalf("first usage touch: %v", err)
	}
	if err := repo.TouchPATLastUsed(context.Background(), issued.PublicID, first.Add(2*time.Minute)); err != nil {
		t.Fatalf("throttled usage touch: %v", err)
	}
	var row PersonalAccessTokenRecord
	if err := repo.db.Where("public_id = ?", issued.PublicID).First(&row).Error; err != nil {
		t.Fatalf("load touched PAT: %v", err)
	}
	if row.LastUsedAt == nil || !row.LastUsedAt.Equal(first) {
		t.Fatalf("last_used_at updated inside five minutes: %v", row.LastUsedAt)
	}
	third := first.Add(6 * time.Minute)
	if err := repo.TouchPATLastUsed(context.Background(), issued.PublicID, third); err != nil {
		t.Fatalf("later usage touch: %v", err)
	}
	if err := repo.db.Where("public_id = ?", issued.PublicID).First(&row).Error; err != nil {
		t.Fatalf("reload touched PAT: %v", err)
	}
	if row.LastUsedAt == nil || !row.LastUsedAt.Equal(third) {
		t.Fatalf("last_used_at did not update after five minutes: %v", row.LastUsedAt)
	}
}

func TestPermanentPersonalAccessTokenRoundTripAndAccountGuards(t *testing.T) {
	for _, state := range []string{"active", "disabled", "decommissioned", "inactive membership", "retired tenant", "revoked"} {
		t.Run(state, func(t *testing.T) {
			repo := patTestRepository(t)
			if err := repo.db.AutoMigrate(&LocalUserRecord{}, &TenantMembershipRecord{}); err != nil {
				t.Fatal(err)
			}
			ensurePATIdentity(t, repo, "tenant-a", "user-a")
			if err := repo.CreateLocalUser(context.Background(), domain.LocalUser{ID: "user-a", Username: "user-a", StorageKey: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, PasswordHash: "!external-only", IdentityProvider: domain.IdentityProviderOAuth2Proxy}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			pepper := []byte("0123456789abcdef0123456789abcdef")
			issued, err := domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{ID: "permanent", TenantID: "tenant-a", UserID: "user-a", Scopes: []string{domain.PATScopeMLflowFull}, NeverExpires: true}, pepper, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CreatePersonalAccessToken(context.Background(), issued.PersonalAccessToken, issued.Digest); err != nil {
				t.Fatal(err)
			}
			var nullCount int64
			if err = repo.db.Model(&PersonalAccessTokenRecord{}).Where("id = ? AND expires_at IS NULL", issued.ID).Count(&nullCount).Error; err != nil || nullCount != 1 {
				t.Fatalf("SQL NULL count=%d err=%v", nullCount, err)
			}
			items, err := repo.ListPersonalAccessTokens(context.Background(), "tenant-a", "user-a")
			if err != nil || len(items) != 1 || items[0].ExpiresAt != nil {
				t.Fatalf("nullable expiry did not roundtrip: %v", err)
			}
			switch state {
			case "disabled":
				err = repo.db.Model(&LocalUserRecord{}).Where("id = ?", "user-a").Update("disabled", true).Error
			case "decommissioned":
				err = repo.db.Model(&LocalUserRecord{}).Where("id = ?", "user-a").Update("decommissioned_at", now).Error
			case "inactive membership":
				err = repo.db.Model(&TenantMembershipRecord{}).Where("identity_id = ?", "user-a").Update("status", domain.MembershipStatusInactive).Error
			case "retired tenant":
				err = repo.db.Model(&TenantRecord{}).Where("id = ?", "tenant-a").Update("retired_at", now).Error
			case "revoked":
				err = repo.RevokePersonalAccessToken(context.Background(), "tenant-a", "user-a", issued.ID, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			authenticator, err := auth.NewPATAuthenticator(repo, pepper, func() time.Time { return now.AddDate(100, 0, 0) })
			if err != nil {
				t.Fatal(err)
			}
			_, err = authenticator.Authenticate(context.Background(), issued.Token)
			if state == "active" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, auth.ErrInvalidPAT) {
				t.Fatalf("state=%s expected invalid PAT got %v", state, err)
			}
		})
	}
}

func TestPersonalAccessTokenRepositoryRejectsInvalidPermanentMetadata(t *testing.T) {
	repo := patTestRepository(t)
	ensurePATIdentity(t, repo, "tenant-a", "user-a")
	for _, user := range []string{"user-a", "integration:one"} {
		for _, scopes := range [][]string{{domain.PATScopeJobsRead}, {domain.PATScopeMLflowFull, domain.PATScopeJobsRead}, {domain.PATScopeMLflowFull}} {
			if user == "user-a" && len(scopes) == 1 && scopes[0] == domain.PATScopeMLflowFull {
				continue
			}
			err := repo.CreatePersonalAccessToken(context.Background(), domain.PersonalAccessToken{ID: "bad", PublicID: "bad", TenantID: "tenant-a", UserID: user, Scopes: scopes}, strings.Repeat("f", 64))
			if err == nil {
				t.Fatalf("invalid permanent metadata accepted user=%s scopes=%v", user, scopes)
			}
		}
	}
}

func TestFinitePersonalAccessTokenCannotFallBackPastDisabledLocalAccount(t *testing.T) {
	for _, column := range []string{"disabled", "decommissioned_at"} {
		t.Run(column, func(t *testing.T) {
			repo := patTestRepository(t)
			if err := repo.db.AutoMigrate(&LocalUserRecord{}, &TenantMembershipRecord{}); err != nil {
				t.Fatal(err)
			}
			ensurePATIdentity(t, repo, "tenant-a", "user-a")
			if err := repo.CreateLocalUser(context.Background(), domain.LocalUser{ID: "user-a", Username: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, PasswordHash: "!external-only"}); err != nil {
				t.Fatal(err)
			}
			issued := issueRepositoryPAT(t, repo, "tenant-a", "user-a", time.Now().UTC())
			var value any = true
			if column == "decommissioned_at" {
				value = time.Now().UTC()
			}
			if err := repo.db.Model(&LocalUserRecord{}).Where("id = ?", "user-a").Update(column, value).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := repo.FindPATByPublicID(context.Background(), issued.PublicID); !errors.Is(err, auth.ErrPATNotFound) {
				t.Fatalf("disabled account fell back to legacy users: %v", err)
			}
		})
	}
}
