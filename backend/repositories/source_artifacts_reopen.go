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

func (r *GormRepository) ReopenSourceArtifactUpload(ctx context.Context, tenantID, userID, artifactID string, expiresAt time.Time) (*domain.SourceArtifact, error) {
	return r.ReopenSourceArtifactUploadWithLimits(ctx, tenantID, userID, artifactID, expiresAt, DefaultSourceArtifactLimits())
}

func (r *GormRepository) ReopenSourceArtifactUploadWithLimits(ctx context.Context, tenantID, userID, artifactID string, expiresAt time.Time, limits SourceArtifactLimits) (*domain.SourceArtifact, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	var result *domain.SourceArtifact
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner UserRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ? AND tenant_id = ?", userID, tenantID).
			First(&owner).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSourceArtifactNotFound
		} else if err != nil {
			return fmt.Errorf("lock source artifact owner for reopen: %w", err)
		}

		var record SourceArtifactRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, artifactID).
			First(&record).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSourceArtifactNotFound
		}
		if err != nil {
			return fmt.Errorf("load source artifact for reopen: %w", err)
		}
		if record.State == string(domain.SourceArtifactReady) {
			var pending int64
			if err := tx.Model(&SourceArtifactRecord{}).
				Where("tenant_id = ? AND user_id = ? AND state = ?", tenantID, userID, string(domain.SourceArtifactPending)).
				Count(&pending).Error; err != nil {
				return fmt.Errorf("count pending source artifacts for reopen: %w", err)
			}
			if pending >= int64(limits.MaxPending) {
				return ErrSourceArtifactQuotaExceeded
			}

			updated := tx.Model(&SourceArtifactRecord{}).
				Where("tenant_id = ? AND user_id = ? AND id = ? AND state = ?", tenantID, userID, artifactID, string(domain.SourceArtifactReady)).
				Updates(map[string]any{
					"state": string(domain.SourceArtifactPending), "completed_at": nil,
					"upload_expires_at": expiresAt.UTC(),
				})
			if updated.Error != nil {
				return fmt.Errorf("reopen source artifact upload: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return ErrSourceArtifactConflict
			}
			record.State = string(domain.SourceArtifactPending)
			record.CompletedAt = nil
			record.UploadExpiresAt = expiresAt.UTC()
		}
		artifact, err := record.toDomain()
		if err != nil {
			return err
		}
		result = artifact
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
