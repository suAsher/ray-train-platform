package repositories

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DatasetPurgePlan names only version-owned storage. Content-addressed shards
// are deliberately excluded, even when their current reference count is zero.
type DatasetPurgePlan struct {
	DatasetID string
	VersionID string
	RunIDs    []string
}

// These identities are not publication history: they only prevent stale
// retries from recreating an identity whose storage has been removed.
type datasetPurgeIdentity struct {
	Kind      string `gorm:"primaryKey"`
	ID        string `gorm:"primaryKey"`
	DatasetID string
	Version   string
}

func (datasetPurgeIdentity) TableName() string { return "dataset_purge_identities" }

func publicationFenceKey(datasetID, versionID string) string { return datasetID + "/" + versionID }

// WithDatasetPublicationWrite holds a shared cross-process fence across the
// publisher's storage side effects as well as its database transitions.
func (r *GormRepository) WithDatasetPublicationWrite(ctx context.Context, datasetID, versionID string, fn func() error) error {
	if fn == nil {
		return ErrDatasetCleanupConflict
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock_shared(hashtextextended(?, 34782))", publicationFenceKey(datasetID, versionID)).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Model(&DatasetVersionRecord{}).Where("id = ? AND dataset_id = ?", versionID, datasetID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrDatasetCleanupNotFound
		}
		return fn()
	})
}

// PurgeFailedDatasetRecord keeps DB records until external cleanup succeeds.
// The external callback must independently fence workers and delete only the
// exact version-owned paths in the plan. Failures remain visible and retryable.
func (r *GormRepository) PurgeFailedDatasetRecord(ctx context.Context, tenantID string, superAdmin bool, datasetID, versionID, actor string, purge func(DatasetPurgePlan) (int, error)) (int, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(versionID) == "" || purge == nil {
		return 0, ErrDatasetCleanupConflict
	}
	removed := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := getManageableDatasetVersionRecord(tx, tenantID, superAdmin, datasetID, versionID); err != nil {
			if errors.Is(err, ErrDatasetPublicationRunNotFound) {
				return ErrDatasetCleanupNotFound
			}
			return err
		}
		if tx.Dialector.Name() == "postgres" {
			var acquired bool
			// Serialize identity INSERTs before they inspect the tombstones. A
			// unique-key wait alone can resume after deletion and recreate an ID.
			if err := tx.Raw("SELECT pg_try_advisory_xact_lock(hashtextextended('dataset-purge-identities', 34783))").Scan(&acquired).Error; err != nil {
				return err
			}
			if !acquired {
				return ErrDatasetCleanupConflict
			}
			if err := tx.Raw("SELECT pg_try_advisory_xact_lock(hashtextextended(?, 34782))", publicationFenceKey(datasetID, versionID)).Scan(&acquired).Error; err != nil {
				return err
			}
			if !acquired {
				return ErrDatasetCleanupConflict
			}
		}
		var runs []DatasetPublicationRunRecord
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("dataset_version_id = ?", versionID).Order("id").Find(&runs).Error; err != nil {
			return err
		}
		var version DatasetVersionRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("id = ? AND dataset_id = ?", versionID, datasetID).First(&version).Error; err != nil {
			return err
		}
		if version.State != "FAILED" {
			return ErrDatasetCleanupConflict
		}
		plan := DatasetPurgePlan{DatasetID: datasetID, VersionID: versionID, RunIDs: []string{}}
		for _, run := range runs {
			if run.State != "FAILED" || run.DatasetID != datasetID {
				return ErrDatasetCleanupConflict
			}
			plan.RunIDs = append(plan.RunIDs, run.ID)
		}
		if err := checkDatasetCleanupDependencies(tx, versionID); err != nil {
			return err
		}
		if err := checkDatasetPurgeObjectReferences(tx, plan); err != nil {
			return err
		}
		var err error
		removed, err = purge(plan)
		if err != nil {
			return err
		}
		identities := []datasetPurgeIdentity{{Kind: "version", ID: version.ID, DatasetID: datasetID, Version: version.Version}}
		for _, run := range runs {
			identities = append(identities, datasetPurgeIdentity{Kind: "run", ID: run.ID, DatasetID: datasetID})
		}
		if err := tx.Create(&identities).Error; err != nil {
			return err
		}
		// Explicit children also make the semantics correct in SQLite test stores
		// where production foreign-key cascades are not installed.
		for _, table := range []string{"dataset_cache_observations", "dataset_publication_partition_attempts", "dataset_version_shards", "dataset_partitions", "dataset_publication_runs"} {
			if err := tx.Table(table).Where("dataset_version_id = ?", versionID).Delete(nil).Error; err != nil {
				return err
			}
		}
		result := tx.Unscoped().Where("id = ? AND state = ?", versionID, "FAILED").Delete(&DatasetVersionRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDatasetCleanupConflict
		}
		return nil
	})
	return removed, datasetCleanupOperationError(err)
}

func checkDatasetPurgeObjectReferences(tx *gorm.DB, p DatasetPurgePlan) error {
	// Inspect all roots and tombstoned versions. A path owned by this purge may
	// never be removed if another version points into it, even malformed legacy
	// references not admitted by today's constraints.
	var keys []string
	if err := tx.Table("dataset_versions").Where("id <> ? AND manifest_object_key IS NOT NULL", p.VersionID).Pluck("manifest_object_key", &keys).Error; err != nil {
		return err
	}
	var shards []string
	if err := tx.Table("dataset_version_shards").Where("dataset_version_id <> ?", p.VersionID).Pluck("object_key", &shards).Error; err != nil {
		return err
	}
	for _, key := range append(keys, shards...) {
		if strings.HasSuffix(key, "/"+p.DatasetID+"/manifests/"+p.VersionID+".parquet") || strings.Contains(key, "/"+p.DatasetID+"/publication/"+p.VersionID+"/") {
			return ErrDatasetCleanupConflict
		}
		for _, runID := range p.RunIDs {
			if strings.Contains(key, "/"+p.DatasetID+"/temp/"+runID+"/") {
				return ErrDatasetCleanupConflict
			}
		}
	}
	return nil
}
