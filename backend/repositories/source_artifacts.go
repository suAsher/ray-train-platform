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
	ErrSourceArtifactNotFound = errors.New("source artifact not found")
	ErrSourceArtifactConflict = errors.New("source artifact conflict")
)

type SourceArtifactRecord struct {
	ID               string `gorm:"primaryKey"`
	TenantID         string `gorm:"index"`
	UserID           string `gorm:"index"`
	StorageRoot      string `gorm:"column:storage_root"`
	SHA256           string `gorm:"index"`
	SizeBytes        int64
	ObjectKey        string
	State            string
	UploadExpiresAt  time.Time
	CompletedAt      *time.Time
	LastReferencedAt *time.Time
	CreatedAt        time.Time
}

func (SourceArtifactRecord) TableName() string { return "source_artifacts" }

type SourceArtifactRequestRecord struct {
	TenantID        string `gorm:"primaryKey"`
	UserID          string `gorm:"primaryKey"`
	ClientRequestID string `gorm:"primaryKey"`
	ArtifactID      string `gorm:"index"`
	CreatedAt       time.Time
}

func (SourceArtifactRequestRecord) TableName() string { return "source_artifact_requests" }

func (r *GormRepository) CreateOrReuseSourceArtifact(ctx context.Context, artifact *domain.SourceArtifact) (*domain.SourceArtifact, error) {
	return r.CreateOrReuseSourceArtifactWithLimits(ctx, artifact, DefaultSourceArtifactLimits())
}

func (r *GormRepository) GetSourceArtifact(ctx context.Context, tenantID, userID, artifactID string) (*domain.SourceArtifact, error) {
	var record SourceArtifactRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, artifactID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSourceArtifactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get source artifact: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) GetSourceArtifactByClientRequestID(ctx context.Context, tenantID, userID, clientRequestID string) (*domain.SourceArtifact, error) {
	var record SourceArtifactRecord
	err := r.db.WithContext(ctx).Table("source_artifacts").
		Select("source_artifacts.*").
		Joins("JOIN source_artifact_requests ON source_artifact_requests.artifact_id = source_artifacts.id AND source_artifact_requests.tenant_id = source_artifacts.tenant_id AND source_artifact_requests.user_id = source_artifacts.user_id").
		Where("source_artifact_requests.tenant_id = ? AND source_artifact_requests.user_id = ? AND source_artifact_requests.client_request_id = ?", tenantID, userID, clientRequestID).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSourceArtifactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get source artifact request: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) MarkSourceArtifactReady(ctx context.Context, tenantID, userID, artifactID string, completedAt time.Time) (*domain.SourceArtifact, error) {
	var result *domain.SourceArtifact
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record SourceArtifactRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, artifactID).First(&record).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSourceArtifactNotFound
		}
		if err != nil {
			return fmt.Errorf("load source artifact for completion: %w", err)
		}
		artifact, err := record.toDomain()
		if err != nil {
			return err
		}
		ready, err := artifact.MarkReady(completedAt)
		if err != nil {
			return fmt.Errorf("mark source artifact ready: %w", err)
		}
		if artifact.State == domain.SourceArtifactPending {
			updates := map[string]any{"state": string(domain.SourceArtifactReady), "completed_at": ready.CompletedAt}
			updated := tx.Model(&SourceArtifactRecord{}).
				Where("tenant_id = ? AND user_id = ? AND id = ? AND state = ?", tenantID, userID, artifactID, string(domain.SourceArtifactPending)).
				Updates(updates)
			if updated.Error != nil {
				return fmt.Errorf("complete source artifact: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return ErrSourceArtifactConflict
			}
		}
		result = &ready
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func sourceArtifactRecordFromDomain(artifact domain.SourceArtifact) SourceArtifactRecord {
	return SourceArtifactRecord{
		ID: artifact.ID, TenantID: artifact.TenantID, UserID: artifact.UserID, StorageRoot: artifact.StorageRoot,
		SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, ObjectKey: artifact.ObjectKey,
		State: string(artifact.State), UploadExpiresAt: artifact.UploadExpiresAt,
		CompletedAt: artifact.CompletedAt, LastReferencedAt: artifact.LastReferencedAt, CreatedAt: artifact.CreatedAt,
	}
}

func (record SourceArtifactRecord) toDomain() (*domain.SourceArtifact, error) {
	artifact := &domain.SourceArtifact{
		ID: record.ID, TenantID: record.TenantID, UserID: record.UserID, StorageRoot: record.StorageRoot,
		SHA256: record.SHA256, SizeBytes: record.SizeBytes, ObjectKey: record.ObjectKey,
		State: domain.SourceArtifactState(record.State), UploadExpiresAt: record.UploadExpiresAt,
		CompletedAt: record.CompletedAt, LastReferencedAt: record.LastReferencedAt, CreatedAt: record.CreatedAt,
	}
	if err := artifact.Validate(); err != nil {
		return nil, fmt.Errorf("invalid stored source artifact: %w", err)
	}
	return artifact, nil
}
