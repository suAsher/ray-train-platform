package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var ErrWorkspaceSnapshotNotFound = errors.New("workspace snapshot not found")

// WorkspaceSnapshotRecord stays private to persistence. The object-store
// prefix is deterministically derived by domain.WorkspaceSnapshotPrefix.
type WorkspaceSnapshotRecord struct {
	ID         string `gorm:"primaryKey"`
	TenantID   string `gorm:"column:tenant_id;index"`
	UserID     string `gorm:"column:user_id;index"`
	SourcePath string `gorm:"column:source_path"`
	FileCount  int    `gorm:"column:file_count"`
	CreatedAt  time.Time
}

func (WorkspaceSnapshotRecord) TableName() string { return "workspace_snapshots" }

func (r *GormRepository) CreateWorkspaceSnapshot(ctx context.Context, snapshot domain.WorkspaceSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = time.Now().UTC()
	}
	record := workspaceSnapshotRecordFromDomain(snapshot)
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create workspace snapshot: %w", err)
	}
	return nil
}

func (r *GormRepository) ListWorkspaceSnapshots(ctx context.Context, tenantID, userID string, limit int) ([]domain.WorkspaceSnapshot, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var records []WorkspaceSnapshotRecord
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list workspace snapshots: %w", err)
	}
	snapshots := make([]domain.WorkspaceSnapshot, 0, len(records))
	for _, record := range records {
		snapshot, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (r *GormRepository) GetWorkspaceSnapshot(ctx context.Context, tenantID, userID, id string) (*domain.WorkspaceSnapshot, error) {
	var record WorkspaceSnapshotRecord
	err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkspaceSnapshotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace snapshot: %w", err)
	}
	snapshot, err := record.toDomain()
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func workspaceSnapshotRecordFromDomain(snapshot domain.WorkspaceSnapshot) WorkspaceSnapshotRecord {
	return WorkspaceSnapshotRecord{
		ID: snapshot.ID, TenantID: snapshot.TenantID, UserID: snapshot.UserID,
		SourcePath: snapshot.SourcePath, FileCount: snapshot.FileCount, CreatedAt: snapshot.CreatedAt,
	}
}

func (record WorkspaceSnapshotRecord) toDomain() (domain.WorkspaceSnapshot, error) {
	snapshot := domain.WorkspaceSnapshot{
		ID: record.ID, TenantID: record.TenantID, UserID: record.UserID,
		SourcePath: record.SourcePath, FileCount: record.FileCount, CreatedAt: record.CreatedAt,
	}
	if err := snapshot.Validate(); err != nil {
		return domain.WorkspaceSnapshot{}, fmt.Errorf("invalid stored workspace snapshot: %w", err)
	}
	return snapshot, nil
}
