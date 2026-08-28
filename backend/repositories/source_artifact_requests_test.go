package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

const testSourceRequestID = "source-request-0123456789abcdef01234567"

func pendingRequestArtifact(t *testing.T, id, tenant, user string, size int64, expires time.Time) *domain.SourceArtifact {
	t.Helper()
	artifact, err := domain.NewRequestScopedSourceArtifact(domain.SourceArtifactInput{
		ID: id, TenantID: tenant, UserID: user, SHA256: repositoryArtifactDigest, SizeBytes: size,
	}, expires, expires.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return &artifact
}

func TestSourceArtifactRequestDoesNotBindAnUnrelatedDigestReuse(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	legacy := pendingArtifact(t, "artifact-legacy-b", "tenant-a", "user-a", 100, now.Add(15*time.Minute))
	legacyCreated, err := repo.CreateOrReuseSourceArtifact(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	requested := pendingRequestArtifact(t, "artifact-request-a", "tenant-a", "user-a", 100, now.Add(20*time.Minute))
	created, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), requested, testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != requested.ID || created.ID == legacyCreated.ID {
		t.Fatalf("request was bound to legacy digest reuse: legacy=%q request=%q", legacyCreated.ID, created.ID)
	}
	if created.ObjectKey == legacyCreated.ObjectKey {
		t.Fatalf("request artifact reused immutable legacy object key %q", created.ObjectKey)
	}
}

func TestLegacyArtifactIgnoresEarlierRequestScopedArtifactAndKeepsQuotaAccounting(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	for _, requestState := range []domain.SourceArtifactState{domain.SourceArtifactReady, domain.SourceArtifactPending} {
		t.Run(string(requestState), func(t *testing.T) {
			repo := artifactTestRepository(t)
			ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
			requested, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingRequestArtifact(t, "artifact-request-first", "tenant-a", "user-a", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
			if err != nil {
				t.Fatal(err)
			}
			if requestState == domain.SourceArtifactReady {
				requested, err = repo.MarkSourceArtifactReady(context.Background(), "tenant-a", "user-a", requested.ID, now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
			}
			limits := SourceArtifactLimits{MaxPending: 2, QuotaBytes: 1000}
			legacyInput := pendingArtifact(t, "artifact-legacy-after-request", "tenant-a", "user-a", 100, now.Add(20*time.Minute))
			legacy, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), legacyInput, limits)
			if err != nil {
				t.Fatalf("legacy create after %s request artifact: %v", requestState, err)
			}
			if legacy.ID != legacyInput.ID || legacy.ObjectKey == requested.ObjectKey || legacy.ObjectKey != legacyInput.ObjectKey {
				t.Fatalf("legacy artifact mismatch: request=%q/%q legacy=%q/%q want=%q", requested.ID, requested.ObjectKey, legacy.ID, legacy.ObjectKey, legacyInput.ObjectKey)
			}
			duplicate := pendingArtifact(t, "artifact-legacy-retry", "tenant-a", "user-a", 100, now.Add(30*time.Minute))
			reused, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), duplicate, limits)
			if err != nil || reused.ID != legacy.ID || reused.ObjectKey != legacy.ObjectKey {
				t.Fatalf("legacy retry did not reuse canonical legacy artifact: reused=%+v err=%v", reused, err)
			}
			var pending int64
			if err := repo.db.Model(&SourceArtifactRecord{}).Where("tenant_id = ? AND user_id = ? AND state = ?", "tenant-a", "user-a", string(domain.SourceArtifactPending)).Count(&pending).Error; err != nil {
				t.Fatal(err)
			}
			wantPending := int64(2)
			if requestState == domain.SourceArtifactReady {
				wantPending = 1
			}
			if pending != wantPending {
				t.Fatalf("pending quota count=%d want=%d after %s request plus legacy reuse", pending, wantPending, requestState)
			}
			var usage struct {
				Count int64
				Total int64
			}
			if err := repo.db.Model(&SourceArtifactRecord{}).
				Select("COUNT(*) AS count, COALESCE(SUM(size_bytes), 0) AS total").
				Where("tenant_id = ? AND user_id = ?", "tenant-a", "user-a").
				Scan(&usage).Error; err != nil {
				t.Fatal(err)
			}
			if usage.Count != 2 || usage.Total != 200 {
				t.Fatalf("owner quota usage=%+v want count=2 total=200", usage)
			}
		})
	}
}

func TestLegacyArtifactRepositoryRejectsRequestScopedObjectKey(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	now := time.Date(2026, 8, 28, 9, 15, 0, 0, time.UTC)
	requestArtifact, err := domain.NewRequestScopedSourceArtifact(domain.SourceArtifactInput{
		ID: "artifact-request-only", TenantID: "tenant-a", UserID: "user-a",
		SHA256: repositoryArtifactDigest, SizeBytes: 100,
	}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOrReuseSourceArtifactWithLimits(context.Background(), &requestArtifact, DefaultSourceArtifactLimits()); !errors.Is(err, ErrSourceArtifactConflict) {
		t.Fatalf("legacy repository accepted request-scoped object key: %v", err)
	}
}

func TestSourceArtifactRequestIsIdempotentWithinOwnerAndPayload(t *testing.T) {
	repo := artifactTestRepository(t)
	ensureArtifactIdentity(t, repo, "tenant-a", "user-a")
	now := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	first := pendingRequestArtifact(t, "artifact-first", "tenant-a", "user-a", 100, now.Add(15*time.Minute))
	created, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), first, testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	retry := pendingRequestArtifact(t, "artifact-random-retry", "tenant-a", "user-a", 100, now.Add(30*time.Minute))
	reused, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), retry, testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != created.ID || reused.ObjectKey != created.ObjectKey || reused.UploadExpiresAt != retry.UploadExpiresAt {
		t.Fatalf("request retry mismatch: created=%q/%q reused=%q/%q expiry=%s", created.ID, created.ObjectKey, reused.ID, reused.ObjectKey, reused.UploadExpiresAt)
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
	first, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingRequestArtifact(t, "artifact-a", "tenant-a", "user-a", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingRequestArtifact(t, "artifact-b", "tenant-b", "user-b", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
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
	_, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), pendingRequestArtifact(t, "artifact-a", "tenant-a", "user-a", 100, now.Add(15*time.Minute)), testSourceRequestID, DefaultSourceArtifactLimits())
	if err != nil {
		t.Fatal(err)
	}
	changed := pendingRequestArtifact(t, "artifact-b", "tenant-a", "user-a", 101, now.Add(15*time.Minute))
	changed.SHA256 = strings.Repeat("b", 64)
	changed.ObjectKey = strings.Replace(changed.ObjectKey, repositoryArtifactDigest, changed.SHA256, 1)
	if _, err := repo.CreateSourceArtifactForRequestWithLimits(context.Background(), changed, testSourceRequestID, DefaultSourceArtifactLimits()); !errors.Is(err, ErrSourceArtifactConflict) {
		t.Fatalf("changed request payload error=%v", err)
	}
	if _, err := repo.GetSourceArtifactByClientRequestID(context.Background(), "tenant-missing", "user-a", testSourceRequestID); !errors.Is(err, ErrSourceArtifactNotFound) {
		t.Fatalf("missing tenant request lookup leaked existence: %v", err)
	}
}
