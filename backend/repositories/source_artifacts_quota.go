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
	if err := limits.validate(); err != nil {
		return nil, err
	}
	incoming := sourceArtifactRecordFromDomain(*artifact)
	var result *domain.SourceArtifact
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			Where("tenant_id = ? AND user_id = ? AND sha256 = ?", artifact.TenantID, artifact.UserID, artifact.SHA256).
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

		var usage struct {
			Pending int64
			Total   int64
		}
		if err := tx.Model(&SourceArtifactRecord{}).
			Select("COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0) AS pending, COALESCE(SUM(size_bytes), 0) AS total", string(domain.SourceArtifactPending)).
			Where("tenant_id = ? AND user_id = ?", artifact.TenantID, artifact.UserID).
			Scan(&usage).Error; err != nil {
			return fmt.Errorf("load source artifact owner usage: %w", err)
		}
		if usage.Pending >= int64(limits.MaxPending) || artifact.SizeBytes > limits.QuotaBytes-usage.Total {
			return ErrSourceArtifactQuotaExceeded
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
