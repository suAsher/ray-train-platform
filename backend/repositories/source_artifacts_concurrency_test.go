package repositories

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func TestSourceArtifactRepositoryReloadsWhenPendingRefreshLosesReadyRace(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-race", "user-race")
	firstExpiry := time.Date(2026, 8, 10, 2, 15, 0, 0, time.UTC)
	created, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-race", "tenant-race", "user-race", 100, firstExpiry))
	if err != nil {
		t.Fatal(err)
	}
	completedAt := firstExpiry.Add(-time.Minute)
	callbackName := "test:source-artifact-ready-race"
	if err := repo.db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "source_artifacts" {
			return
		}
		tx.Session(&gorm.Session{NewDB: true}).Exec(
			"UPDATE source_artifacts SET state = ?, completed_at = ? WHERE id = ?",
			string(domain.SourceArtifactReady), completedAt, created.ID,
		)
		tx.RowsAffected = 0
	}); err != nil {
		t.Fatalf("register race callback: %v", err)
	}
	defer repo.db.Callback().Update().Remove(callbackName)

	reused, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "artifact-new", "tenant-race", "user-race", 100, firstExpiry.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if reused.State != domain.SourceArtifactReady || reused.CompletedAt == nil || !reused.CompletedAt.Equal(completedAt) {
		t.Fatalf("stale pending artifact returned after ready race: state=%q completed=%v", reused.State, reused.CompletedAt)
	}
}
