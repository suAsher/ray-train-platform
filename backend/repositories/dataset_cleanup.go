package repositories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDatasetCleanupNotFound = errors.New("dataset cleanup target not found")
	ErrDatasetCleanupConflict = errors.New("dataset cleanup target is not safely deletable")
)

// DeleteFailedDatasetRecord retains immutable identities and all storage data.
// Run rows are locked before the version, matching controller retry/callbacks.
// The API must establish the administrator role; this method enforces ownership.
func (r *GormRepository) DeleteFailedDatasetRecord(ctx context.Context, tenantID string, superAdmin bool, datasetID, versionID, actor string, publicationOnly bool) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(versionID) == "" {
		return ErrDatasetCleanupConflict
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Resolve scope before locking to avoid exposing foreign target state.
		if _, err := getManageableDatasetVersionRecord(tx, tenantID, superAdmin, datasetID, versionID); err != nil {
			if errors.Is(err, ErrDatasetPublicationRunNotFound) {
				return ErrDatasetCleanupNotFound
			}
			return err
		}
		var runs []DatasetPublicationRunRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("dataset_version_id = ?", versionID).Order("id").Find(&runs).Error; err != nil {
			return err
		}
		// Preserve SQLSTATE here so contention remains a retryable conflict.
		var version DatasetVersionRecord
		versionQuery := tx.Model(&DatasetVersionRecord{}).Select("dataset_versions.*").
			Joins("JOIN datasets ON datasets.id = dataset_versions.dataset_id").
			Clauses(clause.Locking{Strength: "UPDATE"})
		err := manageableDatasetQuery(versionQuery, tenantID, superAdmin).
			Where("dataset_versions.id = ? AND dataset_versions.dataset_id = ?", versionID, datasetID).First(&version).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDatasetCleanupNotFound
		}
		if err != nil {
			return err
		}
		if version.State != "FAILED" {
			return ErrDatasetCleanupConflict
		}
		if publicationOnly && len(runs) == 0 {
			return ErrDatasetCleanupNotFound
		}
		for _, run := range runs {
			if run.State != "FAILED" || run.DatasetID != datasetID {
				return ErrDatasetCleanupConflict
			}
		}
		if err := checkDatasetCleanupDependencies(tx, versionID); err != nil {
			return err
		}
		now := time.Now().UTC()
		audit := map[string]any{"deleted_at": now, "deleted_by": actor, "updated_at": now}
		result := tx.Model(&DatasetPublicationRunRecord{}).Where("dataset_version_id = ? AND state = ?", versionID, "FAILED").Updates(audit)
		if result.Error != nil {
			return fmt.Errorf("hide failed publication: %w", result.Error)
		}
		if result.RowsAffected != int64(len(runs)) {
			return ErrDatasetCleanupConflict
		}
		if publicationOnly {
			return nil
		}
		result = tx.Model(&DatasetVersionRecord{}).Where("id = ? AND state = ?", versionID, "FAILED").Updates(audit)
		if result.Error != nil {
			return fmt.Errorf("hide failed dataset version: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrDatasetCleanupConflict
		}
		return nil
	})
	return datasetCleanupOperationError(err)
}

func datasetCleanupOperationError(err error) error {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && (state.SQLState() == "55P03" || state.SQLState() == "40P01") {
		return ErrDatasetCleanupConflict
	}
	return err
}

func checkDatasetCleanupDependencies(tx *gorm.DB, versionID string) error {
	// Raw table intentionally includes archived jobs and every tenant.
	var references int64
	if err := tx.Table("training_jobs").Where("dataset_version_id = ?", versionID).Count(&references).Error; err != nil {
		return err
	}
	if references != 0 {
		return ErrDatasetCleanupConflict
	}
	var attempts []DatasetPublicationPartitionAttemptRecord
	// Workers lock an attempt before their trigger locks its version. Never
	// wait for that attempt while holding the version: abort cleanup instead.
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("dataset_version_id = ?", versionID).Order("partition_id").Find(&attempts).Error; err != nil {
		return err
	}
	for _, attempt := range attempts {
		if (attempt.State != "FAILED" && attempt.State != "COMPLETED") || attempt.LeaseOwner != "" || attempt.LeaseExpiresAt != nil {
			return ErrDatasetCleanupConflict
		}
	}
	return nil
}
