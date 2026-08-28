package repositories

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var sourceArtifactClientRequestID = regexp.MustCompile(`^source-request-[0-9a-f]{24}$`)

func (r *GormRepository) CreateSourceArtifactForRequestWithLimits(ctx context.Context, artifact *domain.SourceArtifact, clientRequestID string, limits SourceArtifactLimits) (*domain.SourceArtifact, error) {
	if artifact == nil {
		return nil, fmt.Errorf("source artifact is required")
	}
	if err := artifact.Validate(); err != nil {
		return nil, fmt.Errorf("validate source artifact: %w", err)
	}
	if !sourceArtifactClientRequestID.MatchString(clientRequestID) {
		return nil, fmt.Errorf("client request ID is invalid")
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

		var request SourceArtifactRequestRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND user_id = ? AND client_request_id = ?", artifact.TenantID, artifact.UserID, clientRequestID).
			First(&request).Error
		if err == nil {
			var existing SourceArtifactRecord
			if loadErr := tx.Where("id = ? AND tenant_id = ? AND user_id = ?", request.ArtifactID, artifact.TenantID, artifact.UserID).First(&existing).Error; loadErr != nil {
				if errors.Is(loadErr, gorm.ErrRecordNotFound) {
					return ErrSourceArtifactNotFound
				}
				return fmt.Errorf("load requested source artifact: %w", loadErr)
			}
			converted, reuseErr := reuseSourceArtifactRecord(tx, &existing, artifact)
			if reuseErr != nil {
				return reuseErr
			}
			result = converted
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load source artifact request: %w", err)
		}

		if err := enforceSourceArtifactOwnerQuota(tx, artifact, limits); err != nil {
			return err
		}
		if err := tx.Create(&incoming).Error; err != nil {
			return fmt.Errorf("create requested source artifact: %w", err)
		}
		request = SourceArtifactRequestRecord{
			TenantID: artifact.TenantID, UserID: artifact.UserID, ClientRequestID: clientRequestID,
			ArtifactID: artifact.ID, CreatedAt: artifact.CreatedAt,
		}
		if err := tx.Create(&request).Error; err != nil {
			return fmt.Errorf("create source artifact request: %w", err)
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

func enforceSourceArtifactOwnerQuota(tx *gorm.DB, artifact *domain.SourceArtifact, limits SourceArtifactLimits) error {
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
	return nil
}
