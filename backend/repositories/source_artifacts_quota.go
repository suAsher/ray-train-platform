package repositories

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

const (
	DefaultSourceArtifactMaxPending = 10
	DefaultSourceArtifactQuotaBytes = int64(100 * 1024 * 1024 * 1024)
)

var ErrSourceArtifactQuotaExceeded = errors.New("source artifact owner quota exceeded")

type SourceArtifactLimits struct {
	MaxPending int
	QuotaBytes int64
}

func DefaultSourceArtifactLimits() SourceArtifactLimits {
	return SourceArtifactLimits{MaxPending: DefaultSourceArtifactMaxPending, QuotaBytes: DefaultSourceArtifactQuotaBytes}
}

func (limits SourceArtifactLimits) validate() error {
	if limits.MaxPending < 1 || limits.QuotaBytes < 1 {
		return fmt.Errorf("source artifact limits must be positive")
	}
	return nil
}

func (r *GormRepository) CreateOrReuseSourceArtifactWithLimits(ctx context.Context, artifact *domain.SourceArtifact, limits SourceArtifactLimits) (*domain.SourceArtifact, error) {
	if artifact == nil {
		return nil, fmt.Errorf("source artifact is required")
	}
	if err := artifact.Validate(); err != nil {
		return nil, fmt.Errorf("validate source artifact: %w", err)
	}
	legacyObjectKey, err := domain.SourceArtifactObjectKeyForRoot(artifact.TenantID, artifact.StorageRoot, artifact.SHA256)
	if err != nil {
		return nil, fmt.Errorf("derive legacy source artifact key: %w", err)
	}
	if artifact.ObjectKey != legacyObjectKey {
		return nil, ErrSourceArtifactConflict
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	incoming := sourceArtifactRecordFromDomain(*artifact)
	var result *domain.SourceArtifact
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner UserRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ? AND tenant_id = ?", artifact.UserID, artifact.TenantID).
			First(&owner).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSourceArtifactNotFound
		}
		if err != nil {
			return fmt.Errorf("lock source artifact owner: %w", err)
		}

		var existing SourceArtifactRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND user_id = ? AND storage_root = ? AND sha256 = ? AND size_bytes = ? AND object_key = ?",
				artifact.TenantID, artifact.UserID, artifact.StorageRoot, artifact.SHA256, artifact.SizeBytes, legacyObjectKey).
			Where("NOT EXISTS (SELECT 1 FROM source_artifact_requests WHERE source_artifact_requests.artifact_id = source_artifacts.id AND source_artifact_requests.tenant_id = source_artifacts.tenant_id AND source_artifact_requests.user_id = source_artifacts.user_id)").
			Order("created_at ASC, id ASC").
			First(&existing).Error
		if err == nil {
			converted, reuseErr := reuseSourceArtifactRecord(tx, &existing, artifact)
			if reuseErr != nil {
				return reuseErr
			}
			result = converted
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load existing source artifact: %w", err)
		}

		// A canonical legacy object key is immutable. Detect inconsistent legacy
		// metadata before creating another row that would address the same object.
		var conflicting SourceArtifactRecord
		err = tx.Where("tenant_id = ? AND user_id = ? AND storage_root = ? AND sha256 = ? AND object_key = ?",
			artifact.TenantID, artifact.UserID, artifact.StorageRoot, artifact.SHA256, legacyObjectKey).
			Where("NOT EXISTS (SELECT 1 FROM source_artifact_requests WHERE source_artifact_requests.artifact_id = source_artifacts.id AND source_artifact_requests.tenant_id = source_artifacts.tenant_id AND source_artifact_requests.user_id = source_artifacts.user_id)").
			First(&conflicting).Error
		if err == nil {
			return ErrSourceArtifactConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check legacy source artifact key: %w", err)
		}

		if err := enforceSourceArtifactOwnerQuota(tx, artifact, limits); err != nil {
			return err
		}
		if err := tx.Create(&incoming).Error; err != nil {
			return fmt.Errorf("create source artifact: %w", err)
		}
		converted, err := incoming.toDomain()
		if err != nil {
			return err
		}
		result = converted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func reuseSourceArtifactRecord(tx *gorm.DB, existing *SourceArtifactRecord, artifact *domain.SourceArtifact) (*domain.SourceArtifact, error) {
	if existing.SizeBytes != artifact.SizeBytes || existing.ObjectKey != artifact.ObjectKey {
		return nil, ErrSourceArtifactConflict
	}
	if existing.State == string(domain.SourceArtifactPending) {
		refreshed := tx.Model(&SourceArtifactRecord{}).
			Where("id = ? AND tenant_id = ? AND user_id = ? AND state = ?", existing.ID, existing.TenantID, existing.UserID, string(domain.SourceArtifactPending)).
			Update("upload_expires_at", artifact.UploadExpiresAt)
		if refreshed.Error != nil {
			return nil, fmt.Errorf("refresh source artifact upload expiry: %w", refreshed.Error)
		}
		if refreshed.RowsAffected != 1 {
			var current SourceArtifactRecord
			if err := tx.Where("id = ? AND tenant_id = ? AND user_id = ?", existing.ID, existing.TenantID, existing.UserID).First(&current).Error; err != nil {
				return nil, fmt.Errorf("reload source artifact after refresh race: %w", err)
			}
			if current.State != string(domain.SourceArtifactReady) {
				return nil, ErrSourceArtifactConflict
			}
			return current.toDomain()
		}
		existing.UploadExpiresAt = artifact.UploadExpiresAt
	}
	return existing.toDomain()
}
