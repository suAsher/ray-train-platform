package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var ErrStorageAssetNotFound = errors.New("storage asset not found")

type StorageAssetRecord struct {
	ID            string  `gorm:"primaryKey"`
	TenantID      *string `gorm:"column:tenant_id;index"`
	OwnerUserID   *string `gorm:"column:owner_user_id;index"`
	Name          string
	Description   string
	Kind          string `gorm:"index"`
	Provider      string
	ClaimName     string
	RootPrefix    string
	ReadOnly      bool
	BrowseEnabled bool
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (StorageAssetRecord) TableName() string { return "storage_assets" }

func (r *GormRepository) CreateStorageAsset(ctx context.Context, asset domain.StorageAsset) error {
	canonical, err := asset.Canonical()
	if err != nil {
		return err
	}
	if err := canonical.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	record := StorageAssetRecord{
		ID: canonical.ID, TenantID: optionalID(canonical.TenantID), OwnerUserID: optionalID(canonical.OwnerUserID),
		Name: canonical.Name, Description: canonical.Description, Kind: canonical.Kind, Provider: canonical.Provider,
		ClaimName: canonical.ClaimName, RootPrefix: canonical.RootPrefix, ReadOnly: canonical.ReadOnly,
		BrowseEnabled: canonical.BrowseEnabled, CreatedBy: canonical.CreatedBy, CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create storage asset: %w", err)
	}
	return nil
}

// ListStorageAssets returns exactly the roots a requester may choose: shared
// assets, their tenant's shared assets, and their own private assets.
func (r *GormRepository) ListStorageAssets(ctx context.Context, tenantID, userID, kind string) ([]domain.StorageAsset, error) {
	query := r.db.WithContext(ctx).Model(&StorageAssetRecord{}).
		Where("(tenant_id IS NULL AND owner_user_id IS NULL) OR (tenant_id = ? AND (owner_user_id IS NULL OR owner_user_id = ?))", tenantID, userID)
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	var records []StorageAssetRecord
	if err := query.Order("name ASC, id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list storage assets: %w", err)
	}
	assets := make([]domain.StorageAsset, 0, len(records))
	for _, record := range records {
		asset, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

// GetStorageAsset deliberately applies the same scope predicate as the list
// path. A private asset outside a requester's visibility is indistinguishable
// from an absent asset.
func (r *GormRepository) GetStorageAsset(ctx context.Context, tenantID, userID, id string) (domain.StorageAsset, error) {
	var record StorageAssetRecord
	err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Where("(tenant_id IS NULL AND owner_user_id IS NULL) OR (tenant_id = ? AND (owner_user_id IS NULL OR owner_user_id = ?))", tenantID, userID).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.StorageAsset{}, ErrStorageAssetNotFound
	}
	if err != nil {
		return domain.StorageAsset{}, fmt.Errorf("get storage asset: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) DeleteStorageAsset(ctx context.Context, tenantID, id string, superAdmin bool) error {
	query := r.db.WithContext(ctx).Where("id = ?", id)
	if !superAdmin {
		query = query.Where("tenant_id = ?", tenantID)
	}
	result := query.Delete(&StorageAssetRecord{})
	if result.Error != nil {
		return fmt.Errorf("delete storage asset: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrStorageAssetNotFound
	}
	return nil
}

func (record StorageAssetRecord) toDomain() (domain.StorageAsset, error) {
	asset := domain.StorageAsset{
		ID: record.ID, TenantID: valueOrEmpty(record.TenantID), OwnerUserID: valueOrEmpty(record.OwnerUserID),
		Name: record.Name, Description: record.Description, Kind: record.Kind, Provider: record.Provider,
		ClaimName: record.ClaimName, RootPrefix: record.RootPrefix, ReadOnly: record.ReadOnly,
		BrowseEnabled: record.BrowseEnabled, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if err := asset.Validate(); err != nil {
		return domain.StorageAsset{}, fmt.Errorf("invalid stored storage asset: %w", err)
	}
	return asset, nil
}
