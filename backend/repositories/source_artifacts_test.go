package repositories

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

const repositoryArtifactDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func artifactTestRepository(t *testing.T) *GormRepository {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-artifacts?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&TenantRecord{}, &UserRecord{}, &SourceArtifactRecord{}); err != nil {
		t.Fatalf("migrate artifact database: %v", err)
	}
	return NewGormRepository(database)
}

func ensureArtifactIdentity(t *testing.T, repo *GormRepository, tenant, user string) {
	t.Helper()
	if err := repo.EnsureIdentity(context.Background(), auth.Principal{Subject: user, TenantID: tenant, Username: user, Roles: []string{"Engineer"}}); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
}

func pendingArtifact(t *testing.T, id, tenant, user string, size int64, expires time.Time) *domain.SourceArtifact {
	t.Helper()
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{ID: id, TenantID: tenant, UserID: user, SHA256: repositoryArtifactDigest, SizeBytes: size}, expires, expires.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return &artifact
}

func TestSourceArtifactRepositoryReusesPendingAndRefreshesExpiry(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	firstExpiry := time.Date(2026, 8, 10, 2, 15, 0, 0, time.UTC)
	first, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-1", "tenant-a", "user-a", 100, firstExpiry))
	if err != nil {
		t.Fatal(err)
	}
	secondExpiry := firstExpiry.Add(10 * time.Minute)
	second, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-2", "tenant-a", "user-a", 100, secondExpiry))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.UploadExpiresAt != secondExpiry || second.State != domain.SourceArtifactPending {
		t.Fatalf("pending artifact reuse mismatch: first_id=%q second_id=%q second_state=%q second_expiry=%s", first.ID, second.ID, second.State, second.UploadExpiresAt)
	}
}

func TestSourceArtifactRepositoryReadyReuseDoesNotRefreshOrRegress(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	expires := time.Date(2026, 8, 10, 2, 15, 0, 0, time.UTC)
	created, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-1", "tenant-a", "user-a", 100, expires))
	if err != nil {
		t.Fatal(err)
	}
	completedAt := expires.Add(-time.Minute)
	ready, err := repo.MarkSourceArtifactReady(context.Background(), "tenant-a", "user-a", created.ID, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-2", "tenant-a", "user-a", 100, expires.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != ready.ID || reused.State != domain.SourceArtifactReady || reused.CompletedAt == nil || !reused.CompletedAt.Equal(completedAt) || reused.UploadExpiresAt != expires {
		t.Fatalf("ready artifact changed during reuse: id=%q state=%q completed=%t expiry=%s", reused.ID, reused.State, reused.CompletedAt != nil, reused.UploadExpiresAt)
	}
}

func TestSourceArtifactRepositoryEnforcesOwnerIsolationAndSizeConsistency(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	ensureArtifactIdentity(t, repo, "tenant-b", "user-b")
	expires := time.Now().UTC().Add(15 * time.Minute)
	created, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-a", "tenant-a", "user-a", 100, expires))
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-b", "tenant-b", "user-b", 100, expires))
	if err != nil || other.ID == created.ID {
		otherID := ""
		if other != nil {
			otherID = other.ID
		}
		t.Fatalf("different owner must get isolated artifact: created_id=%q other_id=%q err=%v", created.ID, otherID, err)
	}
	if _, err := repo.GetSourceArtifact(context.Background(), "tenant-b", "user-b", created.ID); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("cross-owner get must look not found: %v", err)
	}
	if _, err := repo.MarkSourceArtifactReady(context.Background(), "tenant-b", "user-b", created.ID, time.Now()); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("cross-owner complete must look not found: %v", err)
	}
	if _, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-c", "tenant-a", "user-a", 101, expires)); !errors.Is(err, ErrSourceArtifactConflict) {
		t.Fatalf("same digest with another size must conflict: %v", err)
	}
}

func TestSourceArtifactRepositoryCompletionIsIdempotent(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant", "user")
	expires := time.Now().UTC().Add(15 * time.Minute)
	created, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact", "tenant", "user", 100, expires))
	if err != nil {
		t.Fatal(err)
	}
	firstTime := expires.Add(-time.Minute)
	first, err := repo.MarkSourceArtifactReady(context.Background(), "tenant", "user", created.ID, firstTime)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.MarkSourceArtifactReady(context.Background(), "tenant", "user", created.ID, firstTime.Add(time.Minute))
	if err != nil || first.CompletedAt == nil || second.CompletedAt == nil || !second.CompletedAt.Equal(*first.CompletedAt) {
		t.Fatalf("repeated completion changed timestamp: first=%v second=%v err=%v", first.CompletedAt, second.CompletedAt, err)
	}
}

func TestSourceArtifactRepositoryKeepsInfrastructureErrorsDistinct(t *testing.T) {
	repo := artifactTestRepository(t)
	sqlDB, err := repo.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = repo.GetSourceArtifact(context.Background(), "tenant", "user", "artifact")
	if err == nil || errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("database failure was hidden as not found: %v", err)
	}
}
