package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var ErrImageNotFound = errors.New("image not found")

type PlatformImageRecord struct {
	ID          string  `gorm:"primaryKey"`
	TenantID    *string `gorm:"column:tenant_id;index"`
	Name        string
	Reference   string
	Kind        string `gorm:"index"`
	Description string
	Framework   string
	IsDefault   bool `gorm:"column:is_default"`
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (PlatformImageRecord) TableName() string { return "platform_images" }

func (r *GormRepository) CreateImage(ctx context.Context, image domain.PlatformImage) error {
	if err := image.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	record := PlatformImageRecord{
		ID: image.ID, TenantID: optionalID(image.TenantID), Name: image.Name,
		Reference: image.Reference, Kind: image.Kind, Description: image.Description,
		Framework: image.Framework, IsDefault: image.IsDefault, CreatedBy: image.CreatedBy,
		CreatedAt: now, UpdatedAt: now,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Only one default per kind, otherwise the form has no deterministic
		// pre-selection.
		if image.IsDefault {
			if err := clearDefaultImage(tx, image.Kind, image.TenantID); err != nil {
				return err
			}
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("create image: %w", err)
		}
		return nil
	})
}

func clearDefaultImage(tx *gorm.DB, kind, tenantID string) error {
	query := tx.Model(&PlatformImageRecord{}).Where("kind = ?", kind)
	if tenantID == "" {
		query = query.Where("tenant_id IS NULL")
	} else {
		query = query.Where("tenant_id = ?", tenantID)
	}
	if err := query.Update("is_default", false).Error; err != nil {
		return fmt.Errorf("clear previous default image: %w", err)
	}
	return nil
}

// ListImages returns the images a tenant may use: its own plus the shared ones.
func (r *GormRepository) ListImages(ctx context.Context, tenantID, kind string) ([]domain.PlatformImage, error) {
	query := r.db.WithContext(ctx).Model(&PlatformImageRecord{}).
		Where("tenant_id IS NULL OR tenant_id = ?", tenantID)
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	var records []PlatformImageRecord
	if err := query.Order("is_default DESC, name ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	images := make([]domain.PlatformImage, 0, len(records))
	for _, record := range records {
		images = append(images, domain.PlatformImage{
			ID: record.ID, TenantID: valueOrEmpty(record.TenantID), Name: record.Name,
			Reference: record.Reference, Kind: record.Kind, Description: record.Description,
			Framework: record.Framework, IsDefault: record.IsDefault, CreatedBy: record.CreatedBy,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		})
	}
	return images, nil
}

// DefaultImage picks what a form should preselect: the tenant's default, else
// any shared default, else nothing.
func (r *GormRepository) DefaultImage(ctx context.Context, tenantID, kind string) (domain.PlatformImage, error) {
	images, err := r.ListImages(ctx, tenantID, kind)
	if err != nil {
		return domain.PlatformImage{}, err
	}
	for _, image := range images {
		if image.IsDefault {
			return image, nil
		}
	}
	if len(images) > 0 {
		return images[0], nil
	}
	return domain.PlatformImage{}, ErrImageNotFound
}

// ImageByReference confirms a reference the user submitted is actually in the
// catalogue for their tenant, so the allowlist cannot be bypassed.
func (r *GormRepository) ImageByReference(ctx context.Context, tenantID, kind, reference string) (domain.PlatformImage, error) {
	images, err := r.ListImages(ctx, tenantID, kind)
	if err != nil {
		return domain.PlatformImage{}, err
	}
	for _, image := range images {
		if image.Reference == reference {
			return image, nil
		}
	}
	return domain.PlatformImage{}, ErrImageNotFound
}

func (r *GormRepository) DeleteImage(ctx context.Context, tenantID, id string, superAdmin bool) error {
	query := r.db.WithContext(ctx).Where("id = ?", id)
	if !superAdmin {
		// A tenant administrator may only remove their own tenant's images,
		// never the shared catalogue.
		query = query.Where("tenant_id = ?", tenantID)
	}
	result := query.Delete(&PlatformImageRecord{})
	if result.Error != nil {
		return fmt.Errorf("delete image: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrImageNotFound
	}
	return nil
}
