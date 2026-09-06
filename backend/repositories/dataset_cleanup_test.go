package repositories

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func TestDatasetCleanupClassifiesLockConflicts(t *testing.T) {
	for _, state := range []string{"55P03", "40P01", "23514", "08006"} {
		t.Run(state, func(t *testing.T) {
			original := fmt.Errorf("transaction: %w", datasetConstraintTestError{state: state})
			got := datasetCleanupOperationError(original)
			if state == "55P03" || state == "40P01" {
				if !errors.Is(got, ErrDatasetCleanupConflict) {
					t.Fatalf("lock error not classified: %v", got)
				}
			} else if got != original {
				t.Fatalf("unrelated error changed: %v", got)
			}
		})
	}
	if datasetCleanupOperationError(nil) != nil {
		t.Fatal("success became an error")
	}
	if datasetCleanupOperationError(ErrDatasetCleanupNotFound) != ErrDatasetCleanupNotFound {
		t.Fatal("not found changed")
	}
}

func seedFailedCleanup(t *testing.T) *GormRepository {
	t.Helper()
	r := datasetRepository(t)
	seedPublicationDatasetVersion(t, r, teamDataset("cleanup-data", "cleanup-data", "team-a"), "cleanup-version")
	now := time.Now().UTC()
	if err := r.db.Model(&DatasetVersionRecord{}).Where("id = ?", "cleanup-version").Update("state", "FAILED").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Create(&DatasetPublicationRunRecord{ID: "cleanup-run", DatasetID: "cleanup-data", DatasetVersionID: "cleanup-version", ExecutionMode: "LEGACY", State: "FAILED", FinishedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDatasetCleanupHidesRecordsAndPreservesTombstones(t *testing.T) {
	for _, only := range []bool{false, true} {
		t.Run(map[bool]string{false: "version", true: "publication"}[only], func(t *testing.T) {
			r := seedFailedCleanup(t)
			ctx := context.Background()
			if err := r.DeleteFailedDatasetRecord(ctx, "team-a", false, "cleanup-data", "cleanup-version", "admin-a", only); err != nil {
				t.Fatal(err)
			}
			var run DatasetPublicationRunRecord
			if err := r.db.First(&run, "id = ?", "cleanup-run").Error; !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("visible run: %v", err)
			}
			if err := r.db.Unscoped().First(&run, "id = ?", "cleanup-run").Error; err != nil || !run.DeletedAt.Valid || run.DeletedBy != "admin-a" {
				t.Fatalf("missing audit: %+v %v", run, err)
			}
			var version DatasetVersionRecord
			if err := r.db.Unscoped().First(&version, "id = ?", "cleanup-version").Error; err != nil || version.DeletedAt.Valid == only {
				t.Fatalf("version tombstone: %+v %v", version, err)
			}
			if _, err := r.EnsureDatasetPublicationRun(ctx, "team-a", false, publicationRunForTest("cleanup-data", "cleanup-version", "cleanup-run")); err == nil {
				t.Fatal("late ensure resurrected run")
			}
			if _, err := r.EnsureDatasetPublicationRun(ctx, "team-a", false, publicationRunForTest("cleanup-data", "cleanup-version", "new-cleanup-run")); err == nil {
				t.Fatal("new run bypassed cleanup")
			}
			if _, err := r.GetDatasetPublicationRunForVersion(ctx, "team-a", false, "cleanup-data", "cleanup-version"); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
				t.Fatalf("publication lookup exposes tombstone: %v", err)
			}
			versions, err := r.ListDatasetVersions(ctx, "team-a", false, "cleanup-data")
			if err != nil || len(versions) != map[bool]int{false: 0, true: 1}[only] {
				t.Fatalf("version list: %+v %v", versions, err)
			}
			if _, _, err := r.RetryDatasetPublicationRun(ctx, "team-a", false, "cleanup-data", "cleanup-version", "cleanup-run", time.Now(), 0); err == nil {
				t.Fatal("late retry resurrected run")
			}
			if !only {
				if err := r.CreateDatasetVersion(ctx, discoveringVersion("cleanup-data", "cleanup-version", "v1")); err == nil {
					t.Fatal("late creation resurrected version")
				}
			}
		})
	}
}

func TestDatasetCleanupRollsBackRunWhenVersionWriteFails(t *testing.T) {
	r := seedFailedCleanup(t)
	if err := r.db.Exec("CREATE TRIGGER reject_cleanup BEFORE UPDATE OF deleted_at ON dataset_versions BEGIN SELECT RAISE(ABORT, 'injected version failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteFailedDatasetRecord(context.Background(), "team-a", false, "cleanup-data", "cleanup-version", "admin-a", false); err == nil {
		t.Fatal("expected injected failure")
	}
	var run DatasetPublicationRunRecord
	if err := r.db.First(&run, "id = ?", "cleanup-run").Error; err != nil || run.DeletedBy != "" {
		t.Fatalf("run change escaped rollback: %+v %v", run, err)
	}
}

func TestDatasetCleanupSuperAdminCanCleanPublicDataset(t *testing.T) {
	r := seedFailedCleanup(t)
	if err := r.db.Model(&DatasetRecord{}).Where("id = ?", "cleanup-data").Update("visibility", "PUBLIC").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteFailedDatasetRecord(context.Background(), "other-team", true, "cleanup-data", "cleanup-version", "super-admin", false); err != nil {
		t.Fatal(err)
	}
}

func TestDatasetCleanupRejectsUnsafeOrUnauthorizedRecords(t *testing.T) {
	for _, scenario := range []string{"ready", "active-run", "unknown-run", "leased", "pending", "reference", "archived-reference", "other-team", "public", "blank-actor"} {
		t.Run(scenario, func(t *testing.T) {
			r := seedFailedCleanup(t)
			tenant, actor := "team-a", "admin-a"
			expected := ErrDatasetCleanupConflict
			switch scenario {
			case "ready":
				r.db.Model(&DatasetVersionRecord{}).Where("id = ?", "cleanup-version").Update("state", domain.DatasetVersionReady)
			case "active-run", "unknown-run":
				r.db.Model(&DatasetPublicationRunRecord{}).Where("id = ?", "cleanup-run").Update("state", scenario)
			case "leased", "pending":
				state := "PENDING"
				if scenario == "leased" {
					state = "LEASED"
				}
				if err := r.db.Create(&DatasetPublicationPartitionAttemptRecord{DatasetVersionID: "cleanup-version", PartitionID: "partition-a", State: state}).Error; err != nil {
					t.Fatal(err)
				}
			case "reference", "archived-reference":
				versionID := "cleanup-version"
				job := JobRecord{ID: "referencing-job", TenantID: "team-a", DatasetVersionID: &versionID}
				if scenario == "archived-reference" {
					now := time.Now()
					job.ArchivedAt = &now
				}
				if err := r.db.Create(&job).Error; err != nil {
					t.Fatal(err)
				}
			case "other-team":
				tenant = "team-b"
				expected = ErrDatasetCleanupNotFound
			case "public":
				r.db.Model(&DatasetRecord{}).Where("id = ?", "cleanup-data").Update("visibility", "PUBLIC")
				expected = ErrDatasetCleanupNotFound
			case "blank-actor":
				actor = " "
			}
			if err := r.DeleteFailedDatasetRecord(context.Background(), tenant, false, "cleanup-data", "cleanup-version", actor, false); !errors.Is(err, expected) {
				t.Fatalf("got %v want %v", err, expected)
			}
			var count int64
			r.db.Model(&DatasetPublicationRunRecord{}).Count(&count)
			if count != 1 {
				t.Fatal("rejected cleanup changed run")
			}
		})
	}
}
