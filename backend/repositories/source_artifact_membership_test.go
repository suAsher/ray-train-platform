package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestSourceArtifactTeamMigration(t *testing.T) {
	for _, operation := range []string{"legacy", "request", "reopen"} {
		t.Run(operation, func(t *testing.T) {
			repo := identityMembershipRepository(t)
			if err := repo.db.AutoMigrate(&SourceArtifactRecord{}, &SourceArtifactRequestRecord{}); err != nil {
				t.Fatal(err)
			}
			verifySourceArtifactTeamMigration(t, repo, operation)
		})
	}
}

func TestSourceArtifactTeamMigrationPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	for _, operation := range []string{"legacy", "request", "reopen"} {
		t.Run(operation, func(t *testing.T) {
			admin := openArtifactPostgresConnection(t, dsn)
			schema := fmt.Sprintf("artifact_membership_%d", time.Now().UnixNano())
			if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
					t.Error(err)
				}
			})
			database := openArtifactPostgresConnection(t, dsn)
			if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
				t.Fatal(err)
			}
			if err := databasepkg.ApplyMigrations(database); err != nil {
				t.Fatal(err)
			}
			repo := NewGormRepository(database)
			seedSourceArtifactMembershipIdentity(t, repo)
			verifySourceArtifactTeamMigration(t, repo, operation)
		})
	}
}

func seedSourceArtifactMembershipIdentity(t *testing.T, repo *GormRepository) {
	t.Helper()
	ctx := context.Background()
	for _, tenant := range []string{"team-a", "team-b"} {
		if err := repo.EnsureIdentity(ctx, auth.Principal{Subject: "seed-" + tenant, Username: "seed-" + tenant, TenantID: tenant, Roles: []string{"Engineer"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.EnsureIdentity(ctx, auth.Principal{Subject: "user-a", Username: "alice", TenantID: "team-a", Roles: []string{"Engineer"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateLocalUser(ctx, domain.LocalUser{ID: "user-a", Username: "alice", StorageKey: "stable-alice", TenantID: "team-a", Roles: []string{"Engineer"}, PasswordHash: "!external-only", IdentityProvider: domain.IdentityProviderOAuth2Proxy}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutTenantMembership(ctx, domain.TenantMembership{IdentityID: "user-a", TenantID: "team-b", Roles: []string{"Engineer"}, Status: domain.MembershipStatusActive}); err != nil {
		t.Fatal(err)
	}
}

func verifySourceArtifactTeamMigration(t *testing.T, repo *GormRepository, operation string) {
	t.Helper()
	ctx := context.Background()
	old := sourceMembershipArtifact(t, "old-artifact", "team-a", false)
	if _, err := repo.CreateOrReuseSourceArtifact(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReassignActiveMembership(ctx, "user-a", "team-a", "team-b", []string{"Engineer"}, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureIdentity(ctx, auth.Principal{Subject: "user-a", Username: "alice", TenantID: "team-b", Roles: []string{"Engineer"}}); err != nil {
		t.Fatal(err)
	}
	input := sourceMembershipArtifact(t, "new-artifact", "team-b", operation == "request")
	if operation == "reopen" {
		ready, err := input.MarkReady(time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.db.Create(sourceArtifactRecordPointer(ready)).Error; err != nil {
			t.Fatal(err)
		}
	}
	stored, err := performSourceMembershipOperation(repo, operation, input)
	if err != nil || stored == nil {
		t.Fatalf("migrated team %s upload failed: %v", operation, err)
	}
	if stored.TenantID != "team-b" || stored.StorageRoot != old.StorageRoot || !strings.HasPrefix(stored.ObjectKey, old.StorageRoot+"workspace/") {
		t.Fatalf("migration changed storage ownership: %+v", stored)
	}
	var user UserRecord
	if err := repo.db.Where("id = ?", "user-a").First(&user).Error; err != nil || user.TenantID != "team-a" {
		t.Fatalf("legacy storage home was rewritten: tenant=%q err=%v", user.TenantID, err)
	}
	if _, err := repo.GetSourceArtifact(ctx, "team-b", "user-a", old.ID); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("new team gained access to old team artifact: %v", err)
	}
	if _, err := performSourceMembershipOperation(repo, operation, sourceMembershipArtifact(t, "stale-artifact", "team-a", operation == "request")); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("inactive old team must be rejected: %v", err)
	}
}

func sourceMembershipArtifact(t *testing.T, id, tenant string, request bool) domain.SourceArtifact {
	t.Helper()
	input := domain.SourceArtifactInput{ID: id, TenantID: tenant, UserID: "user-a", StorageRoot: "ray-train/tenants/team-a/users/stable-alice/", SHA256: repositoryArtifactDigest, SizeBytes: 100}
	now := time.Now().UTC().Truncate(time.Microsecond)
	constructor := domain.NewSourceArtifact
	if request {
		constructor = domain.NewRequestScopedSourceArtifact
	}
	artifact, err := constructor(input, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func sourceArtifactRecordPointer(artifact domain.SourceArtifact) *SourceArtifactRecord {
	record := sourceArtifactRecordFromDomain(artifact)
	return &record
}

func performSourceMembershipOperation(repo *GormRepository, operation string, input domain.SourceArtifact) (*domain.SourceArtifact, error) {
	switch operation {
	case "request":
		return repo.CreateSourceArtifactForRequestWithLimits(context.Background(), &input, "source-request-0123456789abcdef01234567", DefaultSourceArtifactLimits())
	case "reopen":
		return repo.ReopenSourceArtifactUpload(context.Background(), input.TenantID, input.UserID, input.ID, time.Now().UTC().Add(time.Hour))
	default:
		return repo.CreateOrReuseSourceArtifact(context.Background(), &input)
	}
}

