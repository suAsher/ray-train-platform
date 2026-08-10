package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func quotaArtifact(t *testing.T, id, tenant, user, digest string, size int64, expires time.Time) *domain.SourceArtifact {
	t.Helper()
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{ID: id, TenantID: tenant, UserID: user, SHA256: digest, SizeBytes: size}, expires, expires.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return &artifact
}

func TestSourceArtifactRepositoryEnforcesAtomicOwnerQuotaAfterDuplicateReuse(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant", "user")
	expires := time.Now().UTC().Add(15 * time.Minute)
	limits := SourceArtifactLimits{MaxPending: 1, QuotaBytes: 150}
	first := quotaArtifact(t, "first", "tenant", "user", strings.Repeat("a", 64), 100, expires)
	created, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), first, limits)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := quotaArtifact(t, "duplicate", "tenant", "user", strings.Repeat("a", 64), 100, expires.Add(time.Minute))
	reused, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), duplicate, limits)
	if err != nil || reused.ID != created.ID {
		t.Fatalf("duplicate should reuse without consuming quota: reused=%q err=%v", reused.ID, err)
	}
	second := quotaArtifact(t, "second", "tenant", "user", strings.Repeat("b", 64), 50, expires)
	if _, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), second, limits); !errors.Is(err, ErrSourceArtifactQuotaExceeded) {
		t.Fatalf("second pending artifact must exceed limit: %v", err)
	}
	if _, err := repo.MarkSourceArtifactReady(context.Background(), "tenant", "user", created.ID, time.Now().UTC()); err != nil {
		t.Fatalf("mark first ready: %v", err)
	}
	byteLimits := SourceArtifactLimits{MaxPending: 10, QuotaBytes: 149}
	if _, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), second, byteLimits); !errors.Is(err, ErrSourceArtifactQuotaExceeded) {
		t.Fatalf("logical total bytes must exceed quota: %v", err)
	}
}

func TestSourceArtifactRepositoryReopenHonorsPendingLimit(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant", "user")
	ctx := context.Background()
	now := time.Now().UTC()
	limits := SourceArtifactLimits{MaxPending: 1, QuotaBytes: 1 << 20}

	first, err := repo.CreateOrReuseSourceArtifactWithLimits(ctx, quotaArtifact(t, "ready-a", "tenant", "user", strings.Repeat("c", 64), 100, now.Add(15*time.Minute)), limits)
	if err != nil {
		t.Fatalf("create first artifact: %v", err)
	}
	if _, err := repo.MarkSourceArtifactReady(ctx, "tenant", "user", first.ID, now); err != nil {
		t.Fatalf("mark first ready: %v", err)
	}
	second, err := repo.CreateOrReuseSourceArtifactWithLimits(ctx, quotaArtifact(t, "ready-b", "tenant", "user", strings.Repeat("d", 64), 100, now.Add(15*time.Minute)), limits)
	if err != nil {
		t.Fatalf("create second artifact: %v", err)
	}
	if _, err := repo.MarkSourceArtifactReady(ctx, "tenant", "user", second.ID, now); err != nil {
		t.Fatalf("mark second ready: %v", err)
	}

	if _, err := repo.ReopenSourceArtifactUploadWithLimits(ctx, "tenant", "user", first.ID, now.Add(15*time.Minute), limits); err != nil {
		t.Fatalf("reopen first artifact: %v", err)
	}
	if _, err := repo.ReopenSourceArtifactUploadWithLimits(ctx, "tenant", "user", second.ID, now.Add(15*time.Minute), limits); !errors.Is(err, ErrSourceArtifactQuotaExceeded) {
		t.Fatalf("second reopen must enforce pending limit: %v", err)
	}
}
