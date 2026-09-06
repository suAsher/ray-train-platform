package repositories

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestTenantRetirementFailsClosedWithoutResourceTables(t *testing.T) {
	repo := testRepository(t)
	_, err := repo.TenantRetirementPreflight(context.Background(), "tenant-a")
	if err == nil {
		t.Fatal("missing resource inventory must block retirement")
	}
}

func retirementRepository(t *testing.T) *GormRepository {
	t.Helper()
	r := testRepository(t)
	if err := r.db.AutoMigrate(&DatasetRecord{}, &DatasetPublicationRunRecord{}, &SourceArtifactRecord{}, &DataSpaceUploadRecord{}, &DataTransferRecord{}, &PersonalAccessTokenRecord{}, &LocalSessionRecord{}, &AuditLogRecord{}, &PlatformImageRecord{}, &DataMountBindingRecord{}, &StorageAssetRecord{}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTenantRetirementPreservesHistoryAndRevokesCredentials(t *testing.T) {
	r := retirementRepository(t)
	ctx := context.Background()
	job := testJob()
	job.ObservedState = domain.StateSucceeded
	if err := r.Create(ctx, &job, "retirement-history"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := r.db.Create(&PersonalAccessTokenRecord{ID: "pat", PublicID: "pat", TenantID: "tenant-a", UserID: "user-a", ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Create(&LocalSessionRecord{ID: "session", PublicID: "session", TenantID: "tenant-a", UserID: "user-a", ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	p, err := r.RetireTenant(ctx, "tenant-a", "admin", func(context.Context, string) ([]string, error) { return nil, nil })
	if err != nil || p.RetiredAt == nil || p.RetiredBy != "admin" {
		t.Fatalf("retirement=%+v err=%v", p, err)
	}
	for _, table := range []string{"personal_access_tokens", "local_sessions"} {
		var count int64
		r.db.Table(table).Where("tenant_id = ? AND revoked_at IS NULL", "tenant-a").Count(&count)
		if count != 0 {
			t.Fatal("credentials not revoked")
		}
	}
	if _, err := r.Get(ctx, "tenant-a", job.ID); err != nil {
		t.Fatalf("history removed: %v", err)
	}
	var auditCount int64
	r.db.Model(&AuditLogRecord{}).Where("action = ? AND resource_id = ?", "tenant.retired", "tenant-a").Count(&auditCount)
	if auditCount != 1 {
		t.Fatal("missing retirement audit")
	}
	repeat, err := r.RetireTenant(ctx, "tenant-a", "other-admin", func(context.Context, string) ([]string, error) {
		t.Fatal("idempotent retry touched cluster")
		return nil, nil
	})
	if err != nil || repeat.RetiredBy != "admin" {
		t.Fatalf("repeat changed retirement: %+v %v", repeat, err)
	}
	if err := r.WithActiveTenantWrite(ctx, "tenant-a", func() error { t.Fatal("retired write callback invoked"); return nil }); err == nil {
		t.Fatal("retired write permitted")
	}
}

func TestTenantRetirementRechecksJobsAndCluster(t *testing.T) {
	r := retirementRepository(t)
	ctx := context.Background()
	p, err := r.TenantRetirementPreflight(ctx, "tenant-a")
	if err != nil || !p.CanRetire {
		t.Fatalf("preflight=%+v %v", p, err)
	}
	job := testJob()
	if err := r.Create(ctx, &job, "race-job"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RetireTenant(ctx, "tenant-a", "admin", func(context.Context, string) ([]string, error) { return nil, nil }); err == nil {
		t.Fatal("new job did not block retirement")
	}
}

func TestTenantRetirementRejectsMissingClusterCheck(t *testing.T) {
	repo := testRepository(t)
	if _, err := repo.RetireTenant(context.Background(), "tenant-a", "admin", nil); err == nil {
		t.Fatal("retirement requires live cluster inventory")
	}
}

func TestTenantRetirementPreflightReportsRetainedReferences(t *testing.T) {
	r := retirementRepository(t)
	if err := r.db.AutoMigrate(&StorageAssetRecord{}, &PlatformImageRecord{}, &DataMountBindingRecord{}); err != nil {
		t.Fatal(err)
	}
	p, err := r.TenantRetirementPreflight(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"historicalJobs", "sessions", "tokens", "datasets", "images", "mounts", "storageAssets"} {
		if _, exists := p.Counts[key]; !exists {
			t.Errorf("missing retained inventory %s", key)
		}
	}
}
