package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const testSourceRequestID = "source-request-0123456789abcdef01234567"

func TestSourceArtifactRequestDoesNotBindAnUnrelatedDigestReuse(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	legacy := pendingArtifact(t, "artifact-legacy-b", "tenant-a", "user-a", 100, now.Add(15*time.Minute))
	legacyCreated, err := repo.CreateOrReuseSourceArtifact(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	requested := pendingArtifact(t, "artifact-request-a", "tenant-a", "user-a", 100, now.Add(20*time.Minute))
	created, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), requested, testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != requested.ID || created.ID == legacyCreated.ID {
		t.Fatalf("request was bound to legacy digest reuse: legacy=%q request=%q", legacyCreated.ID, created.ID)
	}
}

func TestSourceArtifactRequestIsIdempotentWithinOwnerAndPayload(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	first := pendingArtifact(t, "artifact-first", "tenant-a", "user-a", 100, now.Add(15*time.Minute))
	created, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), first, testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	retry := pendingArtifact(t, "artifact-random-retry", "tenant-a", "user-a", 100, now.Add(30*time.Minute))
	reused, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), retry, testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != created.ID || reused.UploadExpiresAt != retry.UploadExpiresAt {
		t.Fatalf("request retry mismatch: created=%q reused=%q expiry=%s", created.ID, reused.ID, reused.UploadExpiresAt)
	}
	resolved, err := repo.GetSourceArtifactByClientRequestID(context.Background(), "tenant-a", "user-a", testSourceRequestID)
	if err != nil || resolved.ID != created.ID {
		t.Fatalf("resolve request: artifact=%+v err=%v", resolved, err)
	}
}

func TestSourceArtifactRequestIdentityIsOwnerScoped(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	ensureArtifactIdentity(t, repo, "tenant-b", "user-b")
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	first, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingArtifact(t, "artifact-a", "tenant-a", "user-a", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingArtifact(t, "artifact-b", "tenant-b", "user-b", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("different owners shared artifact %q", first.ID)
	}
	if _, err := repo.GetSourceArtifactByClientRequestID(context.Background(), "tenant-b", "user-b", testSourceRequestID); err != nil {
		t.Fatalf("owner B could not resolve its request: %v", err)
	}
	if _, err := repo.GetSourceArtifactByClientRequestID(context.Background(), "tenant-b", "user-a", testSourceRequestID); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("wrong owner request lookup leaked existence: %v", err)
	}
}

func TestSourceArtifactRequestRejectsChangedPayloadWithoutExistenceLeak(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	_, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingArtifact(t, "artifact-a", "tenant-a", "user-a", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	changed := pendingArtifact(t, "artifact-b", "tenant-a", "user-a", 101, now.Add(15*time.Minute))
	changed.SHA256 = strings.Repeat("b", 64)
	changed.ObjectKey = strings.Replace(changed.ObjectKey, repositoryArtifactDigest, changed.SHA256, 1)
	if _, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), changed, testSourceRequestID, DefaultSourceArtifactLimits()); !errors.Is(err, ErrSourceArtifactConflict) {
		t.Fatalf("changed request payload error=%v", err)
	}
	if _, err := repo.GetSourceArtifactByClientRequestID(context.Background(), "tenant-missing", "user-a", testSourceRequestID); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("missing tenant request lookup leaked existence: %v", err)
	}
}
