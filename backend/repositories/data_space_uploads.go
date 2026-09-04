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

type DataSpaceUploadRecord struct {
	ID               string `gorm:"primaryKey"`
	TenantID         string
	UserID           string
	SpaceID          string
	RootPrefix       string
	RelativePath     string
	ContentType      string
	SizeBytes        int64
	PartSizeBytes    int64
	TotalParts       int
	ProviderUploadID string
	State            string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (DataSpaceUploadRecord) TableName() string { return "data_space_uploads" }

type DataSpaceUploadPartRecord struct {
	SessionID  string `gorm:"primaryKey"`
	PartNumber int    `gorm:"primaryKey"`
	SizeBytes  int64
	SHA256     string
	ETag       string `gorm:"column:etag"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (DataSpaceUploadPartRecord) TableName() string { return "data_space_upload_parts" }

func uploadRecord(session domain.DataSpaceUploadSession) DataSpaceUploadRecord {
	return DataSpaceUploadRecord{ID: session.ID, TenantID: session.TenantID, UserID: session.UserID, SpaceID: string(session.SpaceID), RootPrefix: session.RootPrefix, RelativePath: session.RelativePath, ContentType: session.ContentType, SizeBytes: session.SizeBytes, PartSizeBytes: session.PartSizeBytes, TotalParts: session.TotalParts, ProviderUploadID: session.ProviderID, State: string(session.State), ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt}
}

func uploadDomain(record DataSpaceUploadRecord) domain.DataSpaceUploadSession {
	return domain.DataSpaceUploadSession{ID: record.ID, TenantID: record.TenantID, UserID: record.UserID, SpaceID: domain.DataSpaceID(record.SpaceID), RootPrefix: record.RootPrefix, RelativePath: record.RelativePath, ContentType: record.ContentType, SizeBytes: record.SizeBytes, PartSizeBytes: record.PartSizeBytes, TotalParts: record.TotalParts, ProviderID: record.ProviderUploadID, State: domain.DataSpaceUploadState(record.State), ExpiresAt: record.ExpiresAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func uploadPartDomain(record DataSpaceUploadPartRecord) domain.DataSpaceUploadPart {
	return domain.DataSpaceUploadPart{SessionID: record.SessionID, PartNumber: record.PartNumber, SizeBytes: record.SizeBytes, SHA256: record.SHA256, ETag: record.ETag, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func (r *GormRepository) CreateOrResumeDataSpaceUpload(ctx context.Context, session domain.DataSpaceUploadSession) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, bool, error) {
	var result domain.DataSpaceUploadSession
	var parts []domain.DataSpaceUploadPart
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DataSpaceUploadRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND user_id = ? AND space_id = ? AND relative_path = ? AND state IN ?", session.TenantID, session.UserID, string(session.SpaceID), session.RelativePath, []string{string(domain.DataSpaceUploadActive), string(domain.DataSpaceUploadCompleting), string(domain.DataSpaceUploadAborting)}).First(&existing).Error
		if err == nil {
			if existing.State != string(domain.DataSpaceUploadActive) || existing.SizeBytes != session.SizeBytes || existing.PartSizeBytes != session.PartSizeBytes || existing.TotalParts != session.TotalParts || existing.ContentType != session.ContentType {
				return domain.ErrDataSpaceUploadConflict
			}
			result = uploadDomain(existing)
			loaded, err := loadUploadParts(tx, existing.ID)
			if err != nil {
				return err
			}
			parts = loaded
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find active data-space upload: %w", err)
		}
		record := uploadRecord(session)
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("create data-space upload: %w", err)
		}
		result = uploadDomain(record)
		created = true
		return nil
	})
	return result, parts, created, err
}

func (r *GormRepository) GetDataSpaceUpload(ctx context.Context, id, tenantID, userID string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error) {
	var record DataSpaceUploadRecord
	if err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.DataSpaceUploadSession{}, nil, domain.ErrDataSpaceUploadNotFound
		}
		return domain.DataSpaceUploadSession{}, nil, fmt.Errorf("get data-space upload: %w", err)
	}
	parts, err := loadUploadParts(r.db.WithContext(ctx), id)
	return uploadDomain(record), parts, err
}

func (r *GormRepository) FindActiveDataSpaceUpload(ctx context.Context, tenantID, userID string, spaceID domain.DataSpaceID, relativePath string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error) {
	var record DataSpaceUploadRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND space_id = ? AND relative_path = ? AND state = ?", tenantID, userID, string(spaceID), relativePath, string(domain.DataSpaceUploadActive)).First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.DataSpaceUploadSession{}, nil, domain.ErrDataSpaceUploadNotFound
		}
		return domain.DataSpaceUploadSession{}, nil, fmt.Errorf("find data-space upload: %w", err)
	}
	parts, err := loadUploadParts(r.db.WithContext(ctx), record.ID)
	return uploadDomain(record), parts, err
}

func loadUploadParts(tx *gorm.DB, sessionID string) ([]domain.DataSpaceUploadPart, error) {
	var records []DataSpaceUploadPartRecord
	if err := tx.Where("session_id = ?", sessionID).Order("part_number ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list data-space upload parts: %w", err)
	}
	parts := make([]domain.DataSpaceUploadPart, 0, len(records))
	for _, record := range records {
		parts = append(parts, uploadPartDomain(record))
	}
	return parts, nil
}

func (r *GormRepository) RecordDataSpaceUploadPart(ctx context.Context, session domain.DataSpaceUploadSession, part domain.DataSpaceUploadPart, expiresAt time.Time) error {
	expected, err := session.Plan().ExpectedPartSize(part.PartNumber)
	if err != nil || part.SessionID != session.ID || part.SizeBytes != expected || domain.ValidateSourceArtifactSHA256(part.SHA256) != nil || part.ETag == "" || !expiresAt.After(time.Now()) {
		return fmt.Errorf("invalid data-space upload part receipt")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&DataSpaceUploadRecord{}).Where("id = ? AND tenant_id = ? AND user_id = ? AND state = ?", session.ID, session.TenantID, session.UserID, string(domain.DataSpaceUploadActive)).Updates(map[string]any{"expires_at": expiresAt, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return fmt.Errorf("extend data-space upload: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrDataSpaceUploadNotFound
		}
		record := DataSpaceUploadPartRecord{SessionID: part.SessionID, PartNumber: part.PartNumber, SizeBytes: part.SizeBytes, SHA256: part.SHA256, ETag: part.ETag, CreatedAt: part.CreatedAt, UpdatedAt: part.UpdatedAt}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "session_id"}, {Name: "part_number"}}, DoUpdates: clause.Assignments(map[string]any{"size_bytes": part.SizeBytes, "sha256": part.SHA256, "etag": part.ETag, "updated_at": part.UpdatedAt})}).Create(&record).Error
	})
}

func (r *GormRepository) StartDataSpaceUploadCompletion(ctx context.Context, id, tenantID, userID string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error) {
	var session domain.DataSpaceUploadSession
	var parts []domain.DataSpaceUploadPart
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record DataSpaceUploadRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).First(&record).Error; err != nil {
			return domain.ErrDataSpaceUploadNotFound
		}
		if record.State == string(domain.DataSpaceUploadCompleted) {
			session = uploadDomain(record)
			return nil
		}
		if record.State != string(domain.DataSpaceUploadActive) {
			return domain.ErrDataSpaceUploadConflict
		}
		loaded, err := loadUploadParts(tx, id)
		if err != nil {
			return err
		}
		if len(loaded) != record.TotalParts {
			return domain.ErrDataSpaceUploadIncomplete
		}
		plan := uploadDomain(record).Plan()
		for index, part := range loaded {
			expected, err := plan.ExpectedPartSize(index + 1)
			if err != nil || part.PartNumber != index+1 || part.SizeBytes != expected || domain.ValidateSourceArtifactSHA256(part.SHA256) != nil || part.ETag == "" {
				return domain.ErrDataSpaceUploadIncomplete
			}
		}
		if err := tx.Model(&record).Updates(map[string]any{"state": string(domain.DataSpaceUploadCompleting), "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		record.State = string(domain.DataSpaceUploadCompleting)
		session, parts = uploadDomain(record), loaded
		return nil
	})
	return session, parts, err
}

func (r *GormRepository) FinishDataSpaceUploadCompletion(ctx context.Context, id string, success bool) error {
	from, to := domain.DataSpaceUploadCompleting, domain.DataSpaceUploadActive
	if success {
		to = domain.DataSpaceUploadCompleted
	}
	result := r.db.WithContext(ctx).Model(&DataSpaceUploadRecord{}).Where("id = ? AND state = ?", id, string(from)).Updates(map[string]any{"state": string(to), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domain.ErrDataSpaceUploadConflict
	}
	return nil
}

func (r *GormRepository) StartDataSpaceUploadAbort(ctx context.Context, id, tenantID, userID string) (domain.DataSpaceUploadSession, error) {
	var record DataSpaceUploadRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).First(&record).Error; err != nil {
			return domain.ErrDataSpaceUploadNotFound
		}
		if record.State == string(domain.DataSpaceUploadAborted) || record.State == string(domain.DataSpaceUploadCompleted) {
			return nil
		}
		if record.State != string(domain.DataSpaceUploadActive) {
			return domain.ErrDataSpaceUploadConflict
		}
		return tx.Model(&record).Updates(map[string]any{"state": string(domain.DataSpaceUploadAborting), "updated_at": time.Now().UTC()}).Error
	})
	if err == nil && record.State == string(domain.DataSpaceUploadActive) {
		record.State = string(domain.DataSpaceUploadAborting)
	}
	return uploadDomain(record), err
}

func (r *GormRepository) FinishDataSpaceUploadAbort(ctx context.Context, id string, success bool) error {
	to := domain.DataSpaceUploadActive
	if success {
		to = domain.DataSpaceUploadAborted
	}
	result := r.db.WithContext(ctx).Model(&DataSpaceUploadRecord{}).Where("id = ? AND state = ?", id, string(domain.DataSpaceUploadAborting)).Updates(map[string]any{"state": string(to), "updated_at": time.Now().UTC()})
	return result.Error
}

func (r *GormRepository) ClaimExpiredDataSpaceUploads(ctx context.Context, now time.Time, limit int) ([]domain.DataSpaceUploadSession, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var claimed []domain.DataSpaceUploadSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var records []DataSpaceUploadRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state = ? AND expires_at <= ?", string(domain.DataSpaceUploadActive), now).Order("expires_at ASC").Limit(limit).Find(&records).Error; err != nil {
			return err
		}
		for _, record := range records {
			if err := tx.Model(&record).Updates(map[string]any{"state": string(domain.DataSpaceUploadAborting), "updated_at": now}).Error; err != nil {
				return err
			}
			record.State = string(domain.DataSpaceUploadAborting)
			claimed = append(claimed, uploadDomain(record))
		}
		return nil
	})
	return claimed, err
}
