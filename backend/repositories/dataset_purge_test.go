package repositories

import (
	"context"
	"errors"
	"testing"
)

func TestDatasetPublicationWriteChecksParentAndPropagatesCallback(t *testing.T) {
	for _, scenario := range []string{"success", "callback-error", "nil-callback", "missing-version", "wrong-dataset", "hidden-version", "database-error"} {
		t.Run(scenario, func(t *testing.T) {
			r := seedFailedCleanup(t)
			datasetID, versionID := "cleanup-data", "cleanup-version"
			called := false
			callbackErr := errors.New("publisher storage failed")
			callback := func() error {
				called = true
				if scenario == "callback-error" {
					return callbackErr
				}
				return nil
			}
			switch scenario {
			case "nil-callback":
				callback = nil
			case "missing-version":
				versionID = "missing"
			case "wrong-dataset":
				datasetID = "foreign"
			case "hidden-version":
				if err := r.DeleteFailedDatasetRecord(context.Background(), "team-a", false, datasetID, versionID, "admin", false); err != nil {
					t.Fatal(err)
				}
			case "database-error":
				if err := r.db.Exec("DROP TABLE dataset_versions").Error; err != nil {
					t.Fatal(err)
				}
			}
			err := r.WithDatasetPublicationWrite(context.Background(), datasetID, versionID, callback)
			switch scenario {
			case "success":
				if err != nil || !called {
					t.Fatalf("success err=%v called=%v", err, called)
				}
			case "callback-error":
				if !errors.Is(err, callbackErr) || !called {
					t.Fatalf("callback err=%v called=%v", err, called)
				}
			case "nil-callback":
				if !errors.Is(err, ErrDatasetCleanupConflict) || called {
					t.Fatalf("nil callback err=%v", err)
				}
			case "database-error":
				if err == nil || called {
					t.Fatalf("database error lost: %v", err)
				}
			default:
				if !errors.Is(err, ErrDatasetCleanupNotFound) || called {
					t.Fatalf("missing parent err=%v called=%v", err, called)
				}
			}
		})
	}
}

func TestDatasetPurgeRejectsInvalidInputBeforeCleanup(t *testing.T) {
	for _, scenario := range []string{"blank-actor", "blank-dataset", "blank-version", "nil-callback", "foreign-team", "foreign-dataset"} {
		t.Run(scenario, func(t *testing.T) {
			r := seedFailedCleanup(t)
			actor, tenant, datasetID, versionID := "admin", "team-a", "cleanup-data", "cleanup-version"
			called := false
			callback := func(DatasetPurgePlan) (int, error) { called = true; return 0, nil }
			want := ErrDatasetCleanupConflict
			switch scenario {
			case "blank-actor":
				actor = " \t"
			case "blank-dataset":
				datasetID = " "
			case "blank-version":
				versionID = " "
			case "nil-callback":
				callback = nil
			case "foreign-team":
				tenant = "other"
				want = ErrDatasetCleanupNotFound
			case "foreign-dataset":
				datasetID = "other"
				want = ErrDatasetCleanupNotFound
			}
			n, err := r.PurgeFailedDatasetRecord(context.Background(), tenant, false, datasetID, versionID, actor, callback)
			if !errors.Is(err, want) || called || n != 0 {
				t.Fatalf("err=%v want=%v called=%v count=%d", err, want, called, n)
			}
		})
	}
}

func TestDatasetPurgeRemovesRowsOnlyAfterStorageSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "retryable"}[fail], func(t *testing.T) {
			r := seedFailedCleanup(t)
			if err := r.db.AutoMigrate(&datasetPurgeIdentity{}); err != nil {
				t.Fatal(err)
			}
			called := false
			n, err := r.PurgeFailedDatasetRecord(context.Background(), "team-a", false, "cleanup-data", "cleanup-version", "admin", func(p DatasetPurgePlan) (int, error) {
				called = true
				if p.DatasetID != "cleanup-data" || p.VersionID != "cleanup-version" || len(p.RunIDs) != 1 || p.RunIDs[0] != "cleanup-run" {
					t.Fatalf("plan %+v", p)
				}
				if fail {
					return 1, errors.New("storage unavailable")
				}
				return 2, nil
			})
			if !called || (err != nil) != fail {
				t.Fatalf("called=%v n=%d err=%v", called, n, err)
			}
			var versions, runs, identities int64
			r.db.Unscoped().Model(&DatasetVersionRecord{}).Count(&versions)
			r.db.Unscoped().Model(&DatasetPublicationRunRecord{}).Count(&runs)
			r.db.Model(&datasetPurgeIdentity{}).Count(&identities)
			if fail {
				if versions != 1 || runs != 1 || identities != 0 {
					t.Fatal("rollback lost records")
				}
			} else if versions != 0 || runs != 0 || identities != 2 || n != 2 {
				t.Fatalf("records %d %d %d", versions, runs, identities)
			}
		})
	}
}

func TestDatasetPurgeDatabaseFailureKeepsVisibleRecords(t *testing.T) {
	for _, table := range []string{"dataset_purge_identities", "dataset_publication_runs", "dataset_versions"} {
		t.Run(table, func(t *testing.T) {
			r := seedFailedCleanup(t)
			if err := r.db.AutoMigrate(&datasetPurgeIdentity{}); err != nil {
				t.Fatal(err)
			}
			op := "DELETE"
			if table == "dataset_purge_identities" {
				op = "INSERT"
			}
			if err := r.db.Exec("CREATE TRIGGER reject_purge BEFORE " + op + " ON " + table + " BEGIN SELECT RAISE(ABORT, 'injected purge failure'); END").Error; err != nil {
				t.Fatal(err)
			}
			called := false
			_, err := r.PurgeFailedDatasetRecord(context.Background(), "team-a", false, "cleanup-data", "cleanup-version", "admin", func(DatasetPurgePlan) (int, error) { called = true; return 1, nil })
			if err == nil || !called {
				t.Fatalf("err=%v called=%v", err, called)
			}
			var versions, runs, identities int64
			if err := r.db.Model(&DatasetVersionRecord{}).Count(&versions).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.db.Model(&DatasetPublicationRunRecord{}).Count(&runs).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.db.Model(&datasetPurgeIdentity{}).Count(&identities).Error; err != nil {
				t.Fatal(err)
			}
			if versions != 1 || runs != 1 || identities != 0 {
				t.Fatalf("rollback versions=%d runs=%d identities=%d", versions, runs, identities)
			}
		})
	}
}

func TestDatasetPurgePreflightDatabaseErrorsNeverDeleteStorage(t *testing.T) {
	for _, table := range []string{"datasets", "dataset_publication_runs", "dataset_publication_partition_attempts", "dataset_version_shards"} {
		t.Run(table, func(t *testing.T) {
			r := seedFailedCleanup(t)
			if err := r.db.Exec("DROP TABLE " + table).Error; err != nil {
				t.Fatal(err)
			}
			called := false
			_, err := r.PurgeFailedDatasetRecord(context.Background(), "team-a", false, "cleanup-data", "cleanup-version", "admin", func(DatasetPurgePlan) (int, error) { called = true; return 0, nil })
			if err == nil || called {
				t.Fatalf("preflight failure err=%v called=%v", err, called)
			}
		})
	}
}

func TestDatasetPublicationFenceKeysScopeVersionsAndDatasets(t *testing.T) {
	first := publicationFenceKey("data-a", "version-a")
	if first == publicationFenceKey("data-b", "version-a") || first == publicationFenceKey("data-a", "version-b") || first != publicationFenceKey("data-a", "version-a") {
		t.Fatal("publication lock key aliases another dataset/version")
	}
}

func TestDatasetPurgeRejectsReferencedObjectsAndUnsafeState(t *testing.T) {
	for _, scenario := range []string{"ready", "active-run", "leased", "archived-job", "foreign", "shared-manifest", "shared-temp", "shared-publication", "shared-shard-reference", "nil-callback"} {
		t.Run(scenario, func(t *testing.T) {
			r := seedFailedCleanup(t)
			r.db.AutoMigrate(&datasetPurgeIdentity{})
			tenant := "team-a"
			switch scenario {
			case "ready":
				r.db.Model(&DatasetVersionRecord{}).Where("id = ?", "cleanup-version").Update("state", "READY")
			case "active-run":
				r.db.Model(&DatasetPublicationRunRecord{}).Where("id = ?", "cleanup-run").Update("state", "PACKING")
			case "leased":
				r.db.Create(&DatasetPublicationPartitionAttemptRecord{DatasetVersionID: "cleanup-version", PartitionID: "p", State: "LEASED"})
			case "archived-job":
				id := "cleanup-version"
				r.db.Create(&JobRecord{ID: "job", TenantID: tenant, DatasetVersionID: &id})
			case "foreign":
				tenant = "other"
			case "shared-manifest", "shared-temp", "shared-publication", "shared-shard-reference":
				key := "root/cleanup-data/manifests/cleanup-version.parquet"
				if scenario == "shared-temp" {
					key = "root/cleanup-data/temp/cleanup-run/part.bin"
				}
				if scenario == "shared-publication" {
					key = "root/cleanup-data/publication/cleanup-version/partition.json"
				}
				if scenario == "shared-shard-reference" {
					if err := r.db.Create(&DatasetVersionShardRecord{DatasetVersionID: "other-version", ShardSHA256: "legacy", ObjectKey: key}).Error; err != nil {
						t.Fatal(err)
					}
					break
				}
				r.db.Create(&DatasetVersionRecord{ID: "other-version", DatasetID: "cleanup-data", Version: "other", State: "FAILED", ManifestObjectKey: &key})
			}
			called := false
			callback := func(DatasetPurgePlan) (int, error) { called = true; return 0, nil }
			if scenario == "nil-callback" {
				callback = nil
			}
			_, err := r.PurgeFailedDatasetRecord(context.Background(), tenant, false, "cleanup-data", "cleanup-version", "admin", callback)
			if err == nil || called {
				t.Fatalf("unsafe purge err=%v callback=%v", err, called)
			}
		})
	}
}
