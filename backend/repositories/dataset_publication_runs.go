package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var (
	ErrDatasetPublicationRunNotFound    = errors.New("dataset publication run not found")
	ErrDatasetPublicationRunConflict    = errors.New("dataset publication run conflict")
	ErrDatasetPublicationRunUnavailable = errors.New("dataset publication run unavailable")
	errDatasetPublicationRunCASLost     = errors.New("dataset publication run compare-and-swap lost")
)

// EnsureDatasetPublicationRun creates a DISCOVERING run once. A retry with the
// same run, dataset, and version identity returns the current persisted state;
// an existing run ID can never be rebound to a different dataset version.
func (r *GormRepository) EnsureDatasetPublicationRun(
	ctx context.Context,
	tenantID string,
	superAdmin bool,
	run domain.DatasetPublicationRun,
) (domain.DatasetPublicationRun, error) {
	if err := publicationRunContextError(ctx); err != nil {
		return domain.DatasetPublicationRun{}, err
	}
	if err := run.Validate(); err != nil {
		return domain.DatasetPublicationRun{}, fmt.Errorf("%w: invalid run", ErrDatasetPublicationRunConflict)
	}
	if run.State != domain.DatasetVersionDiscovering {
		return domain.DatasetPublicationRun{}, ErrDatasetPublicationRunConflict
	}

	var ensured domain.DatasetPublicationRun
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DatasetPublicationRunRecord
		err := tx.Where("id = ?", run.ID).First(&existing).Error
		if err == nil {
			visible, visibilityErr := manageableDatasetVersionExists(tx, tenantID, superAdmin, existing.DatasetID, existing.DatasetVersionID)
			if visibilityErr != nil {
				return publicationRunDatabaseError(ctx, "check existing publication scope", visibilityErr)
			}
			if !visible {
				return ErrDatasetPublicationRunNotFound
			}
			if existing.DatasetID != run.DatasetID || existing.DatasetVersionID != run.DatasetVersionID {
				return ErrDatasetPublicationRunConflict
			}
			scoped, scopeErr := getManageableDatasetPublicationRunRecord(tx, tenantID, superAdmin, run.DatasetID, run.DatasetVersionID, run.ID)
			if scopeErr != nil {
				if errors.Is(scopeErr, ErrDatasetPublicationRunNotFound) {
					return ErrDatasetPublicationRunConflict
				}
				return scopeErr
			}
			ensured, scopeErr = scoped.toDomain()
			return scopeErr
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return publicationRunDatabaseError(ctx, "load publication run identity", err)
		}

		versionExists, err := manageableDatasetVersionExists(tx, tenantID, superAdmin, run.DatasetID, run.DatasetVersionID)
		if err != nil {
			return publicationRunDatabaseError(ctx, "check publication dataset version", err)
		}
		if !versionExists {
			return ErrDatasetPublicationRunNotFound
		}

		now := time.Now().UTC()
		record := datasetPublicationRunRecordFromDomain(run, now)
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if result.Error != nil {
			return publicationRunDatabaseError(ctx, "create publication run", result.Error)
		}
		if result.RowsAffected == 1 {
			ensured = run
			return nil
		}

		var concurrent DatasetPublicationRunRecord
		if err := tx.Where("id = ?", run.ID).First(&concurrent).Error; err != nil {
			return publicationRunDatabaseError(ctx, "load concurrent publication run", err)
		}
		visible, err := manageableDatasetVersionExists(tx, tenantID, superAdmin, concurrent.DatasetID, concurrent.DatasetVersionID)
		if err != nil {
			return publicationRunDatabaseError(ctx, "check concurrent publication scope", err)
		}
		if !visible {
			return ErrDatasetPublicationRunNotFound
		}
		if concurrent.DatasetID != run.DatasetID || concurrent.DatasetVersionID != run.DatasetVersionID {
			return ErrDatasetPublicationRunConflict
		}
		ensured, err = concurrent.toDomain()
		if err != nil {
			return ErrDatasetPublicationRunUnavailable
		}
		return nil
	})
	if err != nil {
		return domain.DatasetPublicationRun{}, publicationRunOperationError(ctx, err)
	}
	return ensured, nil
}

func (r *GormRepository) GetDatasetPublicationRun(
	ctx context.Context,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
	runID string,
) (domain.DatasetPublicationRun, error) {
	if err := publicationRunContextError(ctx); err != nil {
		return domain.DatasetPublicationRun{}, err
	}
	record, err := getDatasetPublicationRunRecord(r.db.WithContext(ctx), tenantID, superAdmin, datasetID, versionID, runID)
	if err != nil {
		return domain.DatasetPublicationRun{}, publicationRunOperationError(ctx, err)
	}
	run, err := record.toDomain()
	if err != nil {
		return domain.DatasetPublicationRun{}, ErrDatasetPublicationRunUnavailable
	}
	return run, nil
}

// ClaimDatasetPublicationRun is a single-winner DISCOVERING -> STABILIZING
// compare-and-swap. Losing reconcilers receive the current state without an
// error and must not create another publication Job.
func (r *GormRepository) ClaimDatasetPublicationRun(
	ctx context.Context,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
	runID string,
	claimedAt time.Time,
) (domain.DatasetPublicationRun, bool, error) {
	if err := publicationRunContextError(ctx); err != nil {
		return domain.DatasetPublicationRun{}, false, err
	}
	claimedAt = normalizedPublicationRunTime(claimedAt)
	var current domain.DatasetPublicationRun
	claimed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record, err := getManageableDatasetPublicationRunRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, datasetID, versionID, runID)
		if err != nil {
			return err
		}
		current, err = record.toDomain()
		if err != nil {
			return ErrDatasetPublicationRunUnavailable
		}
		if current.State != domain.DatasetVersionDiscovering {
			return nil
		}
		version, err := getManageableDatasetVersionRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, datasetID, versionID)
		if err != nil {
			return err
		}
		if version.State != string(domain.DatasetVersionDiscovering) {
			return ErrDatasetPublicationRunUnavailable
		}
		versionResult := tx.Model(&DatasetVersionRecord{}).
			Where("id = ? AND dataset_id = ? AND state = ?", versionID, datasetID, string(domain.DatasetVersionDiscovering)).
			Updates(map[string]any{"state": string(domain.DatasetVersionStabilizing), "updated_at": claimedAt})
		if versionResult.Error != nil {
			return publicationRunDatabaseError(ctx, "claim dataset version", versionResult.Error)
		}
		if versionResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		runResult := tx.Model(&DatasetPublicationRunRecord{}).
			Where("id = ? AND dataset_id = ? AND dataset_version_id = ? AND state = ?", runID, datasetID, versionID, string(domain.DatasetVersionDiscovering)).
			Updates(map[string]any{
				"state": string(domain.DatasetVersionStabilizing), "started_at": claimedAt,
				"finished_at": nil, "updated_at": claimedAt,
			})
		if runResult.Error != nil {
			return publicationRunDatabaseError(ctx, "claim publication run", runResult.Error)
		}
		if runResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		current.State = domain.DatasetVersionStabilizing
		claimed = true
		return nil
	})
	if errors.Is(err, errDatasetPublicationRunCASLost) {
		current, loadErr := r.GetDatasetPublicationRun(ctx, tenantID, superAdmin, datasetID, versionID, runID)
		return current, false, loadErr
	}
	if err != nil {
		return domain.DatasetPublicationRun{}, false, publicationRunOperationError(ctx, err)
	}
	return current, claimed, nil
}

// CompareAndSwapDatasetPublicationRun advances one legal state edge and writes
// all progress counters in the same statement. READY and FAILED also persist
// the attempt finish time.
func (r *GormRepository) CompareAndSwapDatasetPublicationRun(
	ctx context.Context,
	tenantID string,
	superAdmin bool,
	expectedState domain.DatasetVersionState,
	next domain.DatasetPublicationRun,
	observedAt time.Time,
) (domain.DatasetPublicationRun, bool, error) {
	if err := publicationRunContextError(ctx); err != nil {
		return domain.DatasetPublicationRun{}, false, err
	}
	if next.State == domain.DatasetVersionReady {
		return domain.DatasetPublicationRun{}, false, ErrDatasetPublicationRunConflict
	}
	if err := validatePublicationRunTransition(expectedState, next); err != nil {
		return domain.DatasetPublicationRun{}, false, err
	}
	observedAt = normalizedPublicationRunTime(observedAt)
	var current domain.DatasetPublicationRun
	swapped := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record, err := getManageableDatasetPublicationRunRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, next.DatasetID, next.DatasetVersionID, next.ID)
		if err != nil {
			return err
		}
		current, err = record.toDomain()
		if err != nil {
			return ErrDatasetPublicationRunUnavailable
		}
		if current.State != expectedState {
			return nil
		}
		version, err := getManageableDatasetVersionRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, next.DatasetID, next.DatasetVersionID)
		if err != nil {
			return err
		}
		if version.State != string(expectedState) {
			return ErrDatasetPublicationRunUnavailable
		}
		versionResult := tx.Model(&DatasetVersionRecord{}).
			Where("id = ? AND dataset_id = ? AND state = ?", next.DatasetVersionID, next.DatasetID, string(expectedState)).
			Updates(map[string]any{"state": string(next.State), "updated_at": observedAt})
		if versionResult.Error != nil {
			return publicationRunDatabaseError(ctx, "advance dataset version", versionResult.Error)
		}
		if versionResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		runResult := tx.Model(&DatasetPublicationRunRecord{}).
			Where("id = ? AND dataset_id = ? AND dataset_version_id = ? AND state = ?", next.ID, next.DatasetID, next.DatasetVersionID, string(expectedState)).
			Updates(publicationRunUpdates(next, observedAt))
		if runResult.Error != nil {
			return publicationRunDatabaseError(ctx, "advance publication run", runResult.Error)
		}
		if runResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		current = next
		swapped = true
		return nil
	})
	if errors.Is(err, errDatasetPublicationRunCASLost) {
		current, loadErr := r.GetDatasetPublicationRun(ctx, tenantID, superAdmin, next.DatasetID, next.DatasetVersionID, next.ID)
		return current, false, loadErr
	}
	if err != nil {
		return domain.DatasetPublicationRun{}, false, publicationRunOperationError(ctx, err)
	}
	return current, swapped, nil
}

// FinalizeDatasetPublicationRun atomically commits the immutable publisher
// receipt and transitions both the catalogue version and its run to READY.
// A READY run can therefore never be visible without a resolvable manifest.
func (r *GormRepository) FinalizeDatasetPublicationRun(
	ctx context.Context,
	tenantID string,
	superAdmin bool,
	expectedState domain.DatasetVersionState,
	next domain.DatasetPublicationRun,
	receipt domain.DatasetPublicationReceipt,
	internalPrefix string,
	observedAt time.Time,
) (domain.DatasetPublicationRun, bool, error) {
	if err := publicationRunContextError(ctx); err != nil {
		return domain.DatasetPublicationRun{}, false, err
	}
	if expectedState != domain.DatasetVersionPacking || next.State != domain.DatasetVersionReady ||
		next.DatasetID != receipt.DatasetID || next.DatasetVersionID != receipt.DatasetVersionID {
		return domain.DatasetPublicationRun{}, false, ErrDatasetPublicationRunConflict
	}
	if err := validatePublicationRunTransition(expectedState, next); err != nil {
		return domain.DatasetPublicationRun{}, false, err
	}
	if err := receipt.ValidateWithInternalPrefix(internalPrefix); err != nil {
		current, loadErr := r.GetDatasetPublicationRun(ctx, tenantID, superAdmin, next.DatasetID, next.DatasetVersionID, next.ID)
		if loadErr != nil {
			return domain.DatasetPublicationRun{}, false, loadErr
		}
		return current, false, ErrDatasetPublicationRunConflict
	}
	if next.SourceObjectCount != receipt.SourceObjectCount {
		current, loadErr := r.GetDatasetPublicationRun(ctx, tenantID, superAdmin, next.DatasetID, next.DatasetVersionID, next.ID)
		if loadErr != nil {
			return domain.DatasetPublicationRun{}, false, loadErr
		}
		return current, false, ErrDatasetPublicationRunConflict
	}
	readyVersion := receipt.ReadyVersion()
	observedAt = normalizedPublicationRunTime(observedAt)
	var current domain.DatasetPublicationRun
	swapped := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		runRecord, err := getManageableDatasetPublicationRunRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, next.DatasetID, next.DatasetVersionID, next.ID)
		if err != nil {
			return err
		}
		current, err = runRecord.toDomain()
		if err != nil {
			return ErrDatasetPublicationRunUnavailable
		}
		if current.State != expectedState {
			return nil
		}
		versionRecord, err := getManageableDatasetVersionRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, next.DatasetID, next.DatasetVersionID)
		if err != nil {
			return err
		}
		if versionRecord.State != string(expectedState) || versionRecord.Version != receipt.Version || versionRecord.SchemaVersion != receipt.SchemaVersion {
			return ErrDatasetPublicationRunConflict
		}
		versionResult := tx.Model(&DatasetVersionRecord{}).
			Where("id = ? AND dataset_id = ? AND version = ? AND state = ?", readyVersion.ID, readyVersion.DatasetID, readyVersion.Version, string(expectedState)).
			Updates(map[string]any{
				"state": string(domain.DatasetVersionReady), "manifest_sha256": readyVersion.ManifestSHA256,
				"manifest_object_key": readyVersion.ManifestObjectKey, "schema_version": readyVersion.SchemaVersion,
				"train_samples": readyVersion.TrainSamples, "val_samples": readyVersion.ValSamples,
				"test_samples": readyVersion.TestSamples, "source_object_count": readyVersion.SourceObjectCount,
				"logical_bytes": readyVersion.LogicalBytes, "packed_bytes": readyVersion.PackedBytes,
				"updated_at": observedAt,
			})
		if versionResult.Error != nil {
			return publicationRunDatabaseError(ctx, "finalize dataset version", versionResult.Error)
		}
		if versionResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		runResult := tx.Model(&DatasetPublicationRunRecord{}).
			Where("id = ? AND dataset_id = ? AND dataset_version_id = ? AND state = ?", next.ID, next.DatasetID, next.DatasetVersionID, string(expectedState)).
			Updates(publicationRunUpdates(next, observedAt))
		if runResult.Error != nil {
			return publicationRunDatabaseError(ctx, "finalize publication run", runResult.Error)
		}
		if runResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		current = next
		swapped = true
		return nil
	})
	if errors.Is(err, errDatasetPublicationRunCASLost) {
		current, loadErr := r.GetDatasetPublicationRun(ctx, tenantID, superAdmin, next.DatasetID, next.DatasetVersionID, next.ID)
		return current, false, loadErr
	}
	if err != nil {
		return current, false, publicationRunOperationError(ctx, err)
	}
	return current, swapped, nil
}

// RetryDatasetPublicationRun resets a failed attempt only after the persisted
// finish time has aged past backoff. The state predicate gives concurrent
// callers a single winner without retaining stale attempt progress.
func (r *GormRepository) RetryDatasetPublicationRun(
	ctx context.Context,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
	runID string,
	observedAt time.Time,
	backoff time.Duration,
) (domain.DatasetPublicationRun, bool, error) {
	if err := publicationRunContextError(ctx); err != nil {
		return domain.DatasetPublicationRun{}, false, err
	}
	if backoff < 0 {
		return domain.DatasetPublicationRun{}, false, ErrDatasetPublicationRunConflict
	}
	observedAt = normalizedPublicationRunTime(observedAt)
	eligibleAt := observedAt.Add(-backoff)
	var current domain.DatasetPublicationRun
	retried := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record, err := getManageableDatasetPublicationRunRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, datasetID, versionID, runID)
		if err != nil {
			return err
		}
		current, err = record.toDomain()
		if err != nil {
			return ErrDatasetPublicationRunUnavailable
		}
		if current.State != domain.DatasetVersionFailed || record.FinishedAt == nil || record.FinishedAt.After(eligibleAt) {
			return nil
		}
		version, err := getManageableDatasetVersionRecord(tx.Clauses(clause.Locking{Strength: "UPDATE"}), tenantID, superAdmin, datasetID, versionID)
		if err != nil {
			return err
		}
		if version.State != string(domain.DatasetVersionFailed) {
			return ErrDatasetPublicationRunUnavailable
		}
		versionResult := tx.Model(&DatasetVersionRecord{}).
			Where("id = ? AND dataset_id = ? AND state = ?", versionID, datasetID, string(domain.DatasetVersionFailed)).
			Updates(map[string]any{
				"state": string(domain.DatasetVersionDiscovering), "manifest_sha256": nil,
				"manifest_object_key": nil, "train_samples": 0, "val_samples": 0,
				"test_samples": 0, "source_object_count": 0, "logical_bytes": 0,
				"packed_bytes": 0, "updated_at": observedAt,
			})
		if versionResult.Error != nil {
			return publicationRunDatabaseError(ctx, "retry dataset version", versionResult.Error)
		}
		if versionResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		runResult := tx.Model(&DatasetPublicationRunRecord{}).
			Where("id = ? AND dataset_id = ? AND dataset_version_id = ? AND state = ? AND finished_at IS NOT NULL AND finished_at <= ?", runID, datasetID, versionID, string(domain.DatasetVersionFailed), eligibleAt).
			Updates(map[string]any{
				"state": string(domain.DatasetVersionDiscovering), "total_partitions": 0,
				"completed_partitions": 0, "failed_partitions": 0, "source_object_count": 0,
				"processed_object_count": 0, "failed_object_count": 0, "started_at": nil,
				"finished_at": nil, "updated_at": observedAt,
			})
		if runResult.Error != nil {
			return publicationRunDatabaseError(ctx, "retry publication run", runResult.Error)
		}
		if runResult.RowsAffected != 1 {
			return errDatasetPublicationRunCASLost
		}
		current = domain.DatasetPublicationRun{
			ID: runID, DatasetID: datasetID, DatasetVersionID: versionID,
			State: domain.DatasetVersionDiscovering,
		}
		retried = true
		return nil
	})
	if errors.Is(err, errDatasetPublicationRunCASLost) {
		current, loadErr := r.GetDatasetPublicationRun(ctx, tenantID, superAdmin, datasetID, versionID, runID)
		return current, false, loadErr
	}
	if err != nil {
		return domain.DatasetPublicationRun{}, false, publicationRunOperationError(ctx, err)
	}
	return current, retried, nil
}

func getDatasetPublicationRunRecord(
	database *gorm.DB,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
	runID string,
) (DatasetPublicationRunRecord, error) {
	return getDatasetPublicationRunRecordWithScope(database, tenantID, superAdmin, datasetID, versionID, runID, false)
}

func getManageableDatasetPublicationRunRecord(
	database *gorm.DB,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
	runID string,
) (DatasetPublicationRunRecord, error) {
	return getDatasetPublicationRunRecordWithScope(database, tenantID, superAdmin, datasetID, versionID, runID, true)
}

func getDatasetPublicationRunRecordWithScope(
	database *gorm.DB,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
	runID string,
	mutation bool,
) (DatasetPublicationRunRecord, error) {
	var record DatasetPublicationRunRecord
	query := database.Model(&DatasetPublicationRunRecord{}).
		Select("dataset_publication_runs.*").
		Joins("JOIN dataset_versions ON dataset_versions.id = dataset_publication_runs.dataset_version_id AND dataset_versions.dataset_id = dataset_publication_runs.dataset_id").
		Joins("JOIN datasets ON datasets.id = dataset_publication_runs.dataset_id")
	if mutation {
		query = manageableDatasetQuery(query, tenantID, superAdmin)
	} else {
		query = visibleDatasetQuery(query, tenantID, superAdmin)
	}
	err := query.Where(
		"dataset_publication_runs.id = ? AND dataset_publication_runs.dataset_id = ? AND dataset_publication_runs.dataset_version_id = ?",
		runID, datasetID, versionID,
	).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DatasetPublicationRunRecord{}, ErrDatasetPublicationRunNotFound
	}
	if err != nil {
		return DatasetPublicationRunRecord{}, fmt.Errorf("get dataset publication run: %w", err)
	}
	return record, nil
}

func getManageableDatasetVersionRecord(
	database *gorm.DB,
	tenantID string,
	superAdmin bool,
	datasetID string,
	versionID string,
) (DatasetVersionRecord, error) {
	var record DatasetVersionRecord
	query := database.Model(&DatasetVersionRecord{}).
		Select("dataset_versions.*").
		Joins("JOIN datasets ON datasets.id = dataset_versions.dataset_id")
	query = manageableDatasetQuery(query, tenantID, superAdmin)
	err := query.Where("dataset_versions.id = ? AND dataset_versions.dataset_id = ?", versionID, datasetID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DatasetVersionRecord{}, ErrDatasetPublicationRunNotFound
	}
	if err != nil {
		return DatasetVersionRecord{}, ErrDatasetPublicationRunUnavailable
	}
	return record, nil
}

func manageableDatasetVersionExists(database *gorm.DB, tenantID string, superAdmin bool, datasetID, versionID string) (bool, error) {
	query := database.Table("dataset_versions").
		Joins("JOIN datasets ON datasets.id = dataset_versions.dataset_id")
	query = manageableDatasetQuery(query, tenantID, superAdmin)
	var count int64
	err := query.Where("dataset_versions.id = ? AND dataset_versions.dataset_id = ?", versionID, datasetID).Count(&count).Error
	return count == 1, err
}

func validatePublicationRunTransition(expectedState domain.DatasetVersionState, next domain.DatasetPublicationRun) error {
	if err := next.Validate(); err != nil {
		return fmt.Errorf("%w: invalid progress", ErrDatasetPublicationRunConflict)
	}
	allowed := false
	switch expectedState {
	case domain.DatasetVersionStabilizing:
		allowed = next.State == domain.DatasetVersionValidating || next.State == domain.DatasetVersionFailed
	case domain.DatasetVersionValidating:
		allowed = next.State == domain.DatasetVersionPacking || next.State == domain.DatasetVersionFailed
	case domain.DatasetVersionPacking:
		allowed = next.State == domain.DatasetVersionReady || next.State == domain.DatasetVersionFailed
	}
	if !allowed {
		return ErrDatasetPublicationRunConflict
	}
	if next.State == domain.DatasetVersionReady &&
		(next.CompletedPartitions != next.TotalPartitions || next.FailedPartitions != 0 ||
			next.ProcessedObjectCount != next.SourceObjectCount || next.FailedObjectCount != 0) {
		return ErrDatasetPublicationRunConflict
	}
	return nil
}

func publicationRunUpdates(next domain.DatasetPublicationRun, observedAt time.Time) map[string]any {
	updates := map[string]any{
		"state": string(next.State), "total_partitions": next.TotalPartitions,
		"completed_partitions": next.CompletedPartitions, "failed_partitions": next.FailedPartitions,
		"source_object_count": next.SourceObjectCount, "processed_object_count": next.ProcessedObjectCount,
		"failed_object_count": next.FailedObjectCount, "updated_at": observedAt,
	}
	if next.State == domain.DatasetVersionReady || next.State == domain.DatasetVersionFailed {
		updates["finished_at"] = observedAt
	}
	return updates
}

func datasetPublicationRunRecordFromDomain(run domain.DatasetPublicationRun, now time.Time) DatasetPublicationRunRecord {
	return DatasetPublicationRunRecord{
		ID: run.ID, DatasetID: run.DatasetID, DatasetVersionID: run.DatasetVersionID,
		State: string(run.State), TotalPartitions: run.TotalPartitions,
		CompletedPartitions: run.CompletedPartitions, FailedPartitions: run.FailedPartitions,
		SourceObjectCount: run.SourceObjectCount, ProcessedObjectCount: run.ProcessedObjectCount,
		FailedObjectCount: run.FailedObjectCount, CreatedAt: now, UpdatedAt: now,
	}
}

func (record DatasetPublicationRunRecord) toDomain() (domain.DatasetPublicationRun, error) {
	run := domain.DatasetPublicationRun{
		ID: record.ID, DatasetID: record.DatasetID, DatasetVersionID: record.DatasetVersionID,
		State: domain.DatasetVersionState(record.State), TotalPartitions: record.TotalPartitions,
		CompletedPartitions: record.CompletedPartitions, FailedPartitions: record.FailedPartitions,
		SourceObjectCount: record.SourceObjectCount, ProcessedObjectCount: record.ProcessedObjectCount,
		FailedObjectCount: record.FailedObjectCount,
	}
	if err := run.Validate(); err != nil {
		return domain.DatasetPublicationRun{}, fmt.Errorf("invalid stored dataset publication run: %w", err)
	}
	return run, nil
}

func normalizedPublicationRunTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func publicationRunContextError(ctx context.Context) error {
	if ctx == nil {
		return ErrDatasetPublicationRunConflict
	}
	return ctx.Err()
}

func publicationRunDatabaseError(ctx context.Context, operation string, err error) error {
	if contextErr := publicationRunDependencyContextError(ctx, err); contextErr != nil {
		return contextErr
	}
	_ = operation
	_ = err
	return ErrDatasetPublicationRunUnavailable
}

func publicationRunOperationError(ctx context.Context, err error) error {
	if contextErr := publicationRunDependencyContextError(ctx, err); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, ErrDatasetPublicationRunNotFound) || errors.Is(err, ErrDatasetPublicationRunConflict) || errors.Is(err, ErrDatasetPublicationRunUnavailable) {
		return err
	}
	return ErrDatasetPublicationRunUnavailable
}

func publicationRunDependencyContextError(ctx context.Context, err error) error {
	if contextErr := publicationRunContextError(ctx); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
