package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestSourceArtifactRepositoryReopenReadyUploadIsOwnerScopedAndIdempotent(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	ensureArtifactIdentity(t, repo, "tenant-b", "user-b")
	now := time.Now().UTC()
	created, err := repo.CreateOrReuseSourceArtifact(context.Background(), pendingArtifact(t, "reopen-artifact", "tenant-a", "user-a", 100, now.Add(15*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := repo.MarkSourceArtifactReady(context.Background(), "tenant-a", "user-a", created.ID, now)
	if err != nil || ready.State != domain.SourceArtifactReady {
		t.Fatalf("mark ready: state=%q err=%v", ready.State, err)
	}
	if _, err := repo.ReopenSourceArtifactUpload(context.Background(), "tenant-b", "user-b", created.ID, now.Add(20*time.Minute)); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("cross-owner reopen must look not found: %v", err)
	}
	reopened, err := repo.ReopenSourceArtifactUpload(context.Background(), "tenant-a", "user-a", created.ID, now.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != domain.SourceArtifactPending || reopened.CompletedAt != nil || !reopened.UploadExpiresAt.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("unexpected reopened state: state=%q completed=%v expiry=%s", reopened.State, reopened.CompletedAt, reopened.UploadExpiresAt)
	}
	repeated, err := repo.ReopenSourceArtifactUpload(context.Background(), "tenant-a", "user-a", created.ID, now.Add(30*time.Minute))
	if err != nil || repeated.State != domain.SourceArtifactPending || repeated.CompletedAt != nil {
		t.Fatalf("repeated reopen is not idempotent: state=%q completed=%v err=%v", repeated.State, repeated.CompletedAt, err)
	}
}
