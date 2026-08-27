package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var ErrImageNotFound = errors.New("image not found")

type PlatformImageRecord struct {
	ID                   string  `gorm:"primaryKey"`
	TenantID             *string `gorm:"column:tenant_id;index"`
	Name                 string
	Reference            string
	Kind                 string `gorm:"index"`
	Description          string
	Framework            string
	IsDefault            bool   `gorm:"column:is_default"`
	RayVersion           string `gorm:"column:ray_version"`
	SupportedEnginesJSON string `gorm:"column:supported_engines;type:jsonb"`
	CreatedBy            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (PlatformImageRecord) TableName() string { return "platform_images" }

func (r *GormRepository) CreateImage(ctx context.Context, image domain.PlatformImage) error {
	if err := image.Validate(); err != nil {
		return err
	}
	supportedEngines := append([]domain.TrainingEngine(nil), image.SupportedEngines...)
	supportedEnginesJSON, err := json.Marshal(supportedEngines)
	if err != nil {
		return fmt.Errorf("encode image supported engines: %w", err)
	}
	now := time.Now().UTC()
	record := PlatformImageRecord{
		ID: image.ID, TenantID: optionalID(image.TenantID), Name: image.Name,
		Reference: image.Reference, Kind: image.Kind, Description: image.Description,
		Framework: image.Framework, IsDefault: image.IsDefault, CreatedBy: image.CreatedBy,
		RayVersion: image.RayVersion, SupportedEnginesJSON: string(supportedEnginesJSON),
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
	// A tenant can legitimately see two defaults: its own and the shared
	// fallback. Keep the tenant-specific default first so API/CLI consumers do
	// not accidentally select the shared image based on alphabetical order.
	if err := query.
		Clauses(clause.OrderBy{Expression: clause.Expr{
			SQL:                "is_default DESC, CASE WHEN tenant_id = ? THEN 0 ELSE 1 END ASC, name ASC",
			Vars:               []interface{}{tenantID},
			WithoutParentheses: true,
		}}).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	images := make([]domain.PlatformImage, 0, len(records))
	for _, record := range records {
		image, err := platformImageFromRecord(record)
		if err != nil {
			return nil, fmt.Errorf("decode image %s: %w", record.ID, err)
		}
		images = append(images, image)
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

// SetImageShared moves an image visible to the acting super administrator
// between that administrator's tenant catalogue and the platform catalogue.
// The API reserves this operation for SuperAdmin; the repository still scopes
// the lookup to the actor's tenant plus shared rows to avoid reassigning an
// unrelated team's image by ID.
func (r *GormRepository) SetImageShared(ctx context.Context, tenantID, id string, shared bool, targetTenantID string) (domain.PlatformImage, error) {
	var updated PlatformImageRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record PlatformImageRecord
		err := tx.Where("id = ?", id).
			Where("tenant_id IS NULL OR tenant_id = ?", tenantID).
			First(&record).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrImageNotFound
		}
		if err != nil {
			return fmt.Errorf("find image for scope update: %w", err)
		}

		if shared {
			targetTenantID = ""
		}
		if valueOrEmpty(record.TenantID) == targetTenantID {
			updated = record
			return nil
		}
		if record.IsDefault {
			if err := clearDefaultImage(tx, record.Kind, targetTenantID); err != nil {
				return err
			}
		}
		if err := tx.Model(&PlatformImageRecord{}).Where("id = ?", id).Updates(map[string]any{
			"tenant_id":  optionalID(targetTenantID),
			"updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return fmt.Errorf("update image scope: %w", err)
		}
		if err := tx.Where("id = ?", id).First(&updated).Error; err != nil {
			return fmt.Errorf("read updated image: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.PlatformImage{}, err
	}
	image, err := platformImageFromRecord(updated)
	if err != nil {
		return domain.PlatformImage{}, fmt.Errorf("decode updated image %s: %w", updated.ID, err)
	}
	return image, nil
}

func platformImageFromRecord(record PlatformImageRecord) (domain.PlatformImage, error) {
	var supportedEngines []domain.TrainingEngine
	if err := json.Unmarshal([]byte(record.SupportedEnginesJSON), &supportedEngines); err != nil {
		return domain.PlatformImage{}, fmt.Errorf("decode supported engines: %w", err)
	}
	if len(supportedEngines) == 0 {
		return domain.PlatformImage{}, fmt.Errorf("decode supported engines: expected a nonempty JSON array")
	}
	return domain.PlatformImage{
		ID: record.ID, TenantID: valueOrEmpty(record.TenantID), Name: record.Name,
		Reference: record.Reference, Kind: record.Kind, Description: record.Description,
		Framework: record.Framework, IsDefault: record.IsDefault, CreatedBy: record.CreatedBy,
		RayVersion: record.RayVersion, SupportedEngines: append([]domain.TrainingEngine(nil), supportedEngines...),
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
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
