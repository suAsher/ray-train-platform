package repositories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var (
	ErrDatasetNotFound                 = errors.New("dataset not found")
	ErrDatasetConflict                 = errors.New("dataset conflict")
	ErrDatasetVersionNotFound          = errors.New("dataset version not found")
	ErrDatasetVersionConflict          = errors.New("dataset version conflict")
	ErrDatasetVersionNotReady          = errors.New("dataset version is not ready")
	ErrDatasetCacheObservationConflict = errors.New("dataset cache observation conflict")
)

type DatasetRecord struct {
	ID                 string `gorm:"primaryKey"`
	Slug               string
	Name               string
	Description        string
	SourceSpace        string
	SourceRelativePath string
	OwnerTenantID      *string `gorm:"column:owner_tenant_id;index"`
	Visibility         string  `gorm:"index"`
	SchemaVersion      string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type DatasetVersionRecord struct {
	ID                string  `gorm:"primaryKey"`
	DatasetID         string  `gorm:"column:dataset_id;uniqueIndex:dataset_version_identity"`
	Version           string  `gorm:"uniqueIndex:dataset_version_identity"`
	State             string  `gorm:"index"`
	ManifestSHA256    *string `gorm:"column:manifest_sha256"`
	ManifestObjectKey *string `gorm:"column:manifest_object_key"`
	SchemaVersion     string
	TrainSamples      int64
	ValSamples        int64
	TestSamples       int64
	SourceObjectCount int64
	LogicalBytes      int64
	PackedBytes       int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DatasetPartitionRecord struct {
	ID                   string `gorm:"primaryKey"`
	DatasetVersionID     string `gorm:"column:dataset_version_id;uniqueIndex:dataset_partition_name"`
	Name                 string `gorm:"uniqueIndex:dataset_partition_name"`
	SourceObjectCount    int64
	ProcessedObjectCount int64
	FailedObjectCount    int64
	LogicalBytes         int64
	PackedBytes          int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type DatasetPublicationRunRecord struct {
	ID                   string `gorm:"primaryKey"`
	DatasetID            string `gorm:"column:dataset_id;index"`
	DatasetVersionID     string `gorm:"column:dataset_version_id;index"`
	State                string `gorm:"index"`
	TotalPartitions      int64
	CompletedPartitions  int64
	FailedPartitions     int64
	SourceObjectCount    int64
	ProcessedObjectCount int64
	FailedObjectCount    int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
}

type DatasetVersionShardRecord struct {
	DatasetVersionID string `gorm:"column:dataset_version_id;primaryKey"`
	DatasetID        string `gorm:"column:dataset_id"`
	PartitionID      string
	Split            string
	Ordinal          int64
	ShardSHA256      string `gorm:"column:shard_sha256;primaryKey"`
	ObjectKey        string
	SampleCount      int64
	LogicalBytes     int64
	PackedBytes      int64
	CreatedAt        time.Time
}

type DatasetCacheObservationRecord struct {
	ID                       string `gorm:"primaryKey"`
	DatasetVersionID         string `gorm:"column:dataset_version_id;index"`
	TrainingJobID            string `gorm:"column:training_job_id;index"`
	NodeName                 string
	CacheHitCount            int64
	CacheMissCount           int64
	CacheHitBytes            int64
	CacheMissBytes           int64
	CachedBytes              int64
	EvictedBytes             int64
	ChecksumFailureCount     int64
	PrefetchWaitMilliseconds int64
	CreatedAt                time.Time
}

func (DatasetRecord) TableName() string                 { return "datasets" }
func (DatasetVersionRecord) TableName() string          { return "dataset_versions" }
func (DatasetPartitionRecord) TableName() string        { return "dataset_partitions" }
func (DatasetPublicationRunRecord) TableName() string   { return "dataset_publication_runs" }
func (DatasetVersionShardRecord) TableName() string     { return "dataset_version_shards" }
func (DatasetCacheObservationRecord) TableName() string { return "dataset_cache_observations" }

func (r *GormRepository) CreateDataset(ctx context.Context, dataset domain.Dataset) error {
	if err := dataset.Validate(); err != nil {
		return fmt.Errorf("validate dataset: %w", err)
	}
	now := time.Now().UTC()
	record := datasetRecordFromDomain(dataset, now)
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return fmt.Errorf("create dataset: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDatasetConflict
	}
	return nil
}

func (r *GormRepository) GetDataset(ctx context.Context, tenantID string, superAdmin bool, datasetID string) (domain.Dataset, error) {
	var record DatasetRecord
	query := visibleDatasetQuery(r.db.WithContext(ctx).Model(&DatasetRecord{}), tenantID, superAdmin)
	err := query.Where("datasets.id = ?", datasetID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Dataset{}, ErrDatasetNotFound
	}
	if err != nil {
		return domain.Dataset{}, fmt.Errorf("get dataset: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) ListDatasets(ctx context.Context, tenantID string, superAdmin bool) ([]domain.Dataset, error) {
	var records []DatasetRecord
	query := visibleDatasetQuery(r.db.WithContext(ctx).Model(&DatasetRecord{}), tenantID, superAdmin)
	if err := query.Order("datasets.slug ASC, datasets.id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list datasets: %w", err)
	}
	datasets := make([]domain.Dataset, 0, len(records))
	for _, record := range records {
		dataset, err := record.toDomain()
		if err != nil {
			return nil, fmt.Errorf("decode dataset %s: %w", record.ID, err)
		}
		datasets = append(datasets, dataset)
	}
	return datasets, nil
}

func (r *GormRepository) CreateDatasetVersion(ctx context.Context, version domain.DatasetVersion) error {
	if err := version.Validate(); err != nil {
		return fmt.Errorf("validate dataset version: %w", err)
	}
	if version.State != domain.DatasetVersionDiscovering {
		return fmt.Errorf("%w: new versions must start in DISCOVERING", ErrDatasetVersionConflict)
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var datasetCount int64
		if err := tx.Model(&DatasetRecord{}).Where("id = ?", version.DatasetID).Count(&datasetCount).Error; err != nil {
			return fmt.Errorf("check dataset for version: %w", err)
		}
		if datasetCount != 1 {
			return ErrDatasetNotFound
		}

		now := time.Now().UTC()
		record := datasetVersionRecordFromDomain(version, now)
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if result.Error != nil {
			return fmt.Errorf("create dataset version: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			return nil
		}

		var collisions []DatasetVersionRecord
		if err := tx.Where("id = ? OR (dataset_id = ? AND version = ?)", version.ID, version.DatasetID, version.Version).Find(&collisions).Error; err != nil {
			return fmt.Errorf("load conflicting dataset version: %w", err)
		}
		if len(collisions) == 1 && sameDatasetVersionPayload(collisions[0], record) {
			return nil
		}
		return ErrDatasetVersionConflict
	})
}

func (r *GormRepository) GetDatasetVersion(ctx context.Context, tenantID string, superAdmin bool, datasetID, versionID string) (domain.DatasetVersion, error) {
	var record DatasetVersionRecord
	query := visibleDatasetVersionQuery(r.db.WithContext(ctx), tenantID, superAdmin)
	err := query.Where("dataset_versions.dataset_id = ? AND dataset_versions.id = ?", datasetID, versionID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.DatasetVersion{}, ErrDatasetVersionNotFound
	}
	if err != nil {
		return domain.DatasetVersion{}, fmt.Errorf("get dataset version: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) ListDatasetVersions(ctx context.Context, tenantID string, superAdmin bool, datasetID string) ([]domain.DatasetVersion, error) {
	if _, err := r.GetDataset(ctx, tenantID, superAdmin, datasetID); err != nil {
		return nil, err
	}

	var records []DatasetVersionRecord
	query := visibleDatasetVersionQuery(r.db.WithContext(ctx), tenantID, superAdmin)
	if err := query.Where("dataset_versions.dataset_id = ?", datasetID).
		Order("dataset_versions.created_at DESC, dataset_versions.id DESC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list dataset versions: %w", err)
	}
	versions := make([]domain.DatasetVersion, 0, len(records))
	for _, record := range records {
		version, err := record.toDomain()
		if err != nil {
			return nil, fmt.Errorf("decode dataset version %s: %w", record.ID, err)
		}
		versions = append(versions, version)
	}
	return versions, nil
}

func (r *GormRepository) ResolveReadyDatasetVersion(ctx context.Context, tenantID string, superAdmin bool, datasetID string, selector domain.DatasetVersionSelector) (domain.DatasetVersion, error) {
	hasExplicitID := strings.TrimSpace(selector.VersionID) != ""
	if selector.Latest == hasExplicitID {
		return domain.DatasetVersion{}, fmt.Errorf("%w: choose either latest or an explicit version ID", ErrDatasetVersionNotReady)
	}
	if hasExplicitID {
		if err := domain.ValidateResolvedDatasetVersionID(selector.VersionID); err != nil {
			return domain.DatasetVersion{}, fmt.Errorf("%w: invalid selector", ErrDatasetVersionNotReady)
		}
	}

	var record DatasetVersionRecord
	query := visibleDatasetVersionQuery(r.db.WithContext(ctx), tenantID, superAdmin).
		Where("dataset_versions.dataset_id = ? AND dataset_versions.state = ?", datasetID, string(domain.DatasetVersionReady))
	if selector.Latest {
		query = query.Order("dataset_versions.created_at DESC, dataset_versions.id DESC")
	} else {
		query = query.Where("dataset_versions.id = ?", selector.VersionID)
	}
	err := query.First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.DatasetVersion{}, ErrDatasetVersionNotReady
	}
	if err != nil {
		return domain.DatasetVersion{}, fmt.Errorf("resolve ready dataset version: %w", err)
	}
	version, err := record.toDomain()
	if err != nil {
		return domain.DatasetVersion{}, fmt.Errorf("decode ready dataset version: %w", err)
	}
	return version, nil
}

func (r *GormRepository) UpdateDatasetVersionDraft(ctx context.Context, version domain.DatasetVersion) (domain.DatasetVersion, error) {
	if err := version.Validate(); err != nil {
		return domain.DatasetVersion{}, fmt.Errorf("validate dataset version draft: %w", err)
	}

	var updated domain.DatasetVersion
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentRecord DatasetVersionRecord
		err := tx.Where("id = ? AND dataset_id = ?", version.ID, version.DatasetID).First(&currentRecord).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDatasetVersionNotFound
		}
		if err != nil {
			return fmt.Errorf("load dataset version draft: %w", err)
		}
		current, err := currentRecord.toDomain()
		if err != nil {
			return err
		}
		if current.ID != version.ID || current.DatasetID != version.DatasetID || current.Version != version.Version || current.State != version.State {
			return ErrDatasetVersionConflict
		}
		if !isDatasetVersionDraftState(current.State) {
			return ErrDatasetVersionConflict
		}

		result := tx.Model(&DatasetVersionRecord{}).
			Where("id = ? AND dataset_id = ? AND version = ? AND state = ?", current.ID, current.DatasetID, current.Version, string(current.State)).
			Updates(map[string]any{
				"manifest_sha256":     optionalID(version.ManifestSHA256),
				"manifest_object_key": optionalID(version.ManifestObjectKey),
				"schema_version":      version.SchemaVersion,
				"train_samples":       version.TrainSamples,
				"val_samples":         version.ValSamples,
				"test_samples":        version.TestSamples,
				"source_object_count": version.SourceObjectCount,
				"logical_bytes":       version.LogicalBytes,
				"packed_bytes":        version.PackedBytes,
				"updated_at":          time.Now().UTC(),
			})
		if result.Error != nil {
			return fmt.Errorf("update dataset version draft: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrDatasetVersionConflict
		}
		updated = version
		return nil
	})
	if err != nil {
		return domain.DatasetVersion{}, err
	}
	return updated, nil
}

func (r *GormRepository) TransitionDatasetVersion(ctx context.Context, datasetID, versionID string, next domain.DatasetVersionState) (domain.DatasetVersion, error) {
	var transitioned domain.DatasetVersion
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentRecord DatasetVersionRecord
		err := tx.Where("id = ? AND dataset_id = ?", versionID, datasetID).First(&currentRecord).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDatasetVersionNotFound
		}
		if err != nil {
			return fmt.Errorf("load dataset version for transition: %w", err)
		}
		current, err := currentRecord.toDomain()
		if err != nil {
			return err
		}
		transitioned, err = current.TransitionTo(next)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrDatasetVersionConflict, err)
		}

		result := tx.Model(&DatasetVersionRecord{}).
			Where("id = ? AND dataset_id = ? AND state = ?", current.ID, current.DatasetID, string(current.State)).
			Updates(map[string]any{"state": string(transitioned.State), "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return fmt.Errorf("transition dataset version: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrDatasetVersionConflict
		}
		return nil
	})
	if err != nil {
		return domain.DatasetVersion{}, err
	}
	return transitioned, nil
}

func (r *GormRepository) CountDatasetVersionReferences(ctx context.Context, datasetVersionID string) (int64, error) {
	if err := domain.ValidateResolvedDatasetVersionID(datasetVersionID); err != nil {
		return 0, fmt.Errorf("validate dataset version reference: %w", err)
	}
	var count int64
	if err := r.db.WithContext(ctx).Model(&JobRecord{}).
		Where("dataset_version_id = ?", datasetVersionID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count dataset version references: %w", err)
	}
	return count, nil
}

func (r *GormRepository) RecordDatasetCacheObservation(ctx context.Context, observation domain.DatasetCacheObservation) error {
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("validate dataset cache observation: %w", err)
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var matchingPins int64
		err := tx.Table("training_jobs AS training_job").
			Joins("JOIN dataset_versions AS dataset_version ON dataset_version.id = training_job.dataset_version_id AND dataset_version.dataset_id = training_job.dataset_id").
			Where("training_job.id = ? AND training_job.dataset_version_id = ? AND dataset_version.id = ?", observation.TrainingJobID, observation.DatasetVersionID, observation.DatasetVersionID).
			Count(&matchingPins).Error
		if err != nil {
			return fmt.Errorf("check cache observation job pin: %w", err)
		}
		if matchingPins != 1 {
			return ErrDatasetCacheObservationConflict
		}

		record := datasetCacheObservationRecordFromDomain(observation, time.Now().UTC())
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if result.Error != nil {
			return fmt.Errorf("record dataset cache observation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrDatasetCacheObservationConflict
		}
		return nil
	})
}

func visibleDatasetQuery(query *gorm.DB, tenantID string, superAdmin bool) *gorm.DB {
	if superAdmin {
		return query
	}
	if strings.TrimSpace(tenantID) == "" {
		return query.Where("datasets.id IS NULL")
	}
	return query.Where(
		"datasets.visibility = ? OR (datasets.visibility = ? AND datasets.owner_tenant_id = ?)",
		string(domain.DatasetVisibilityPublic), string(domain.DatasetVisibilityTeam), tenantID,
	)
}

func visibleDatasetVersionQuery(database *gorm.DB, tenantID string, superAdmin bool) *gorm.DB {
	query := database.Model(&DatasetVersionRecord{}).
		Select("dataset_versions.*").
		Joins("JOIN datasets ON datasets.id = dataset_versions.dataset_id")
	return visibleDatasetQuery(query, tenantID, superAdmin)
}

func isDatasetVersionDraftState(state domain.DatasetVersionState) bool {
	switch state {
	case domain.DatasetVersionDiscovering, domain.DatasetVersionStabilizing, domain.DatasetVersionValidating, domain.DatasetVersionPacking:
		return true
	default:
		return false
	}
}

func datasetRecordFromDomain(dataset domain.Dataset, now time.Time) DatasetRecord {
	return DatasetRecord{
		ID: dataset.ID, Slug: dataset.Slug, Name: dataset.Name, Description: dataset.Description,
		SourceSpace: string(dataset.SourceSpace), SourceRelativePath: dataset.SourceRelativePath,
		OwnerTenantID: optionalID(dataset.OwnerTenantID), Visibility: string(dataset.Visibility),
		SchemaVersion: dataset.SchemaVersion, CreatedAt: now, UpdatedAt: now,
	}
}

func (record DatasetRecord) toDomain() (domain.Dataset, error) {
	dataset := domain.Dataset{
		ID: record.ID, Slug: record.Slug, Name: record.Name, Description: record.Description,
		SourceSpace: domain.DataSpaceID(record.SourceSpace), SourceRelativePath: record.SourceRelativePath,
		OwnerTenantID: valueOrEmpty(record.OwnerTenantID), Visibility: domain.DatasetVisibility(record.Visibility),
		SchemaVersion: record.SchemaVersion,
	}
	if err := dataset.Validate(); err != nil {
		return domain.Dataset{}, fmt.Errorf("invalid stored dataset: %w", err)
	}
	return dataset, nil
}

func datasetVersionRecordFromDomain(version domain.DatasetVersion, now time.Time) DatasetVersionRecord {
	return DatasetVersionRecord{
		ID: version.ID, DatasetID: version.DatasetID, Version: version.Version, State: string(version.State),
		ManifestSHA256: optionalID(version.ManifestSHA256), ManifestObjectKey: optionalID(version.ManifestObjectKey),
		SchemaVersion: version.SchemaVersion, TrainSamples: version.TrainSamples, ValSamples: version.ValSamples,
		TestSamples: version.TestSamples, SourceObjectCount: version.SourceObjectCount,
		LogicalBytes: version.LogicalBytes, PackedBytes: version.PackedBytes,
		CreatedAt: now, UpdatedAt: now,
	}
}

func (record DatasetVersionRecord) toDomain() (domain.DatasetVersion, error) {
	version := domain.DatasetVersion{
		ID: record.ID, DatasetID: record.DatasetID, Version: record.Version,
		State: domain.DatasetVersionState(record.State), ManifestSHA256: valueOrEmpty(record.ManifestSHA256),
		ManifestObjectKey: valueOrEmpty(record.ManifestObjectKey), SchemaVersion: record.SchemaVersion,
		TrainSamples: record.TrainSamples, ValSamples: record.ValSamples, TestSamples: record.TestSamples,
		SourceObjectCount: record.SourceObjectCount, LogicalBytes: record.LogicalBytes, PackedBytes: record.PackedBytes,
	}
	if err := version.Validate(); err != nil {
		return domain.DatasetVersion{}, fmt.Errorf("invalid stored dataset version: %w", err)
	}
	return version, nil
}

func sameDatasetVersionPayload(left, right DatasetVersionRecord) bool {
	return left.ID == right.ID &&
		left.DatasetID == right.DatasetID &&
		left.Version == right.Version &&
		left.State == right.State &&
		sameOptionalString(left.ManifestSHA256, right.ManifestSHA256) &&
		sameOptionalString(left.ManifestObjectKey, right.ManifestObjectKey) &&
		left.SchemaVersion == right.SchemaVersion &&
		left.TrainSamples == right.TrainSamples &&
		left.ValSamples == right.ValSamples &&
		left.TestSamples == right.TestSamples &&
		left.SourceObjectCount == right.SourceObjectCount &&
		left.LogicalBytes == right.LogicalBytes &&
		left.PackedBytes == right.PackedBytes
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func datasetCacheObservationRecordFromDomain(observation domain.DatasetCacheObservation, createdAt time.Time) DatasetCacheObservationRecord {
	return DatasetCacheObservationRecord{
		ID: observation.ID, DatasetVersionID: observation.DatasetVersionID,
		TrainingJobID: observation.TrainingJobID, NodeName: observation.NodeName,
		CacheHitCount: observation.CacheHitCount, CacheMissCount: observation.CacheMissCount,
		CacheHitBytes: observation.CacheHitBytes, CacheMissBytes: observation.CacheMissBytes,
		CachedBytes: observation.CachedBytes, EvictedBytes: observation.EvictedBytes,
		ChecksumFailureCount:     observation.ChecksumFailureCount,
		PrefetchWaitMilliseconds: observation.PrefetchWaitMilliseconds, CreatedAt: createdAt,
	}
}

func (record DatasetCacheObservationRecord) toDomain() (domain.DatasetCacheObservation, error) {
	observation := domain.DatasetCacheObservation{
		ID: record.ID, DatasetVersionID: record.DatasetVersionID,
		TrainingJobID: record.TrainingJobID, NodeName: record.NodeName,
		CacheHitCount: record.CacheHitCount, CacheMissCount: record.CacheMissCount,
		CacheHitBytes: record.CacheHitBytes, CacheMissBytes: record.CacheMissBytes,
		CachedBytes: record.CachedBytes, EvictedBytes: record.EvictedBytes,
		ChecksumFailureCount:     record.ChecksumFailureCount,
		PrefetchWaitMilliseconds: record.PrefetchWaitMilliseconds,
	}
	if err := observation.Validate(); err != nil {
		return domain.DatasetCacheObservation{}, fmt.Errorf("invalid stored dataset cache observation: %w", err)
	}
	return observation, nil
}
