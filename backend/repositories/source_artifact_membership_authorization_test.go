package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func TestSourceArtifactMembershipAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		change func(*gorm.DB) error
	}{
		{"inactive membership", func(db *gorm.DB) error {
			return db.Model(&TenantMembershipRecord{}).Where("identity_id = ? AND tenant_id = ?", "user-a", "team-a").Update("status", domain.MembershipStatusInactive).Error
		}},
		{"missing membership", func(db *gorm.DB) error {
			return db.Where("identity_id = ? AND tenant_id = ?", "user-a", "team-a").Delete(&TenantMembershipRecord{}).Error
		}},
		{"disabled owner", func(db *gorm.DB) error {
			return db.Model(&LocalUserRecord{}).Where("id = ?", "user-a").Update("disabled", true).Error
		}},
		{"decommissioned owner", func(db *gorm.DB) error {
			return db.Model(&LocalUserRecord{}).Where("id = ?", "user-a").Update("decommissioned_at", time.Now().UTC()).Error
		}},
		{"retired team", func(db *gorm.DB) error {
			return db.Model(&TenantRecord{}).Where("id = ?", "team-a").Update("retired_at", time.Now().UTC()).Error
		}},
	}
	for _, test := range tests {
		for _, operation := range []string{"legacy", "request", "reopen"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				repo := identityMembershipRepository(t)
				if err := repo.db.AutoMigrate(&SourceArtifactRecord{}, &SourceArtifactRequestRecord{}); err != nil {
					t.Fatal(err)
				}
				input := sourceMembershipArtifact(t, "authorization-artifact", "team-a", operation == "request")
				if operation == "reopen" {
					ready, err := input.MarkReady(time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
					if err := repo.db.Create(sourceArtifactRecordPointer(ready)).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := test.change(repo.db); err != nil {
					t.Fatal(err)
				}
				if _, err := performSourceMembershipOperation(repo, operation, input); !errors.Is(err, ErrSourceArtifactNotFound) {
					t.Fatalf("unauthorized source operation must be rejected, got %v", err)
				}
			})
		}
	}
}

func TestSourceArtifactStillActiveOtherTeamMembership(t *testing.T) {
	repo := identityMembershipRepository(t)
	if err := repo.db.AutoMigrate(&SourceArtifactRecord{}, &SourceArtifactRequestRecord{}); err != nil {
		t.Fatal(err)
	}
	// Interactive team-a remains selected, but a team-b PAT is authorized by
	// its own still-active membership rather than the interactive selection.
	input := sourceMembershipArtifact(t, "other-active-team", "team-b", false)
	if _, err := repo.CreateOrReuseSourceArtifact(context.Background(), &input); err != nil {
		t.Fatalf("still-active other team upload rejected: %v", err)
	}
}

func TestSourceArtifactLegacyOwnerWithoutPlatformAccount(t *testing.T) {
	for _, schema := range []string{"legacy", "platform"} {
		t.Run(schema, func(t *testing.T) {
			repo := artifactTestRepository(t)
			if schema == "platform" {
				if err := repo.db.AutoMigrate(&LocalUserRecord{}, &TenantMembershipRecord{}); err != nil {
					t.Fatal(err)
				}
			}
			ensureArtifactIdentity(t, repo, "team-a", "user-a")
			input := sourceMembershipArtifact(t, "legacy-owner", "team-a", false)
			if _, err := repo.CreateOrReuseSourceArtifact(context.Background(), &input); err != nil {
				t.Fatal(err)
			}
			foreign := sourceMembershipArtifact(t, "foreign-team", "team-b", false)
			if _, err := repo.CreateOrReuseSourceArtifact(context.Background(), &foreign); !errors.Is(err, ErrSourceArtifactNotFound) {
				t.Fatalf("legacy owner crossed team boundary: %v", err)
			}
		})
	}
}
