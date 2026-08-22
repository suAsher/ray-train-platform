package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var ErrDataMountBindingNotFound = errors.New("data mount binding not found")

// DataMountBindingRecord is private platform inventory. It deliberately is
// not returned directly by HTTP handlers because claim names, root prefixes,
// and CSI attributes are implementation details.
type DataMountBindingRecord struct {
	ID                   string  `gorm:"primaryKey"`
	TenantID             *string `gorm:"column:tenant_id;index"`
	UserID               *string `gorm:"column:user_id;index"`
	StorageKey           string  `gorm:"column:storage_key"`
	Scope                string  `gorm:"index"`
	SpaceID              string  `gorm:"column:space_id;index"`
	ClaimName            string  `gorm:"column:claim_name"`
	ServiceAccountName   string  `gorm:"column:service_account_name"`
	Driver               string
	VolumeAttributesJSON string `gorm:"column:volume_attributes_json;type:jsonb"`
	RootPrefix           string `gorm:"column:root_prefix"`
	ReadOnly             bool   `gorm:"column:read_only"`
	Status               string `gorm:"index"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (DataMountBindingRecord) TableName() string { return "data_mount_bindings" }

// EnsureTenantSharedDataBindings installs the two tenant-local PVC adapters
// for the read-only team and public roots. Both records are tenant-scoped:
// public data has a global TOS prefix, but a Kubernetes PVC cannot be shared
// across namespaces.
func (r *GormRepository) EnsureTenantSharedDataBindings(ctx context.Context, requested ...domain.DataMountBinding) ([]domain.DataMountBinding, error) {
	if len(requested) != 2 {
		return nil, fmt.Errorf("exactly team-shared and public bindings are required")
	}
	result := make([]domain.DataMountBinding, 0, len(requested))
	for _, binding := range requested {
		if binding.Scope != domain.DataMountScopeTenant || (binding.SpaceID != domain.DataSpaceTeamShared && binding.SpaceID != domain.DataSpacePublic) {
			return nil, fmt.Errorf("only tenant-scoped team-shared and public bindings are supported")
		}
		if err := binding.Validate(); err != nil {
			return nil, err
		}
		var existing DataMountBindingRecord
		err := r.db.WithContext(ctx).
			Where("tenant_id = ? AND scope = ? AND space_id = ?", binding.TenantID, domain.DataMountScopeTenant, binding.SpaceID).
			First(&existing).Error
		switch {
		case err == nil:
			stored, conversionErr := existing.toDomain()
			if conversionErr != nil {
				return nil, conversionErr
			}
			result = append(result, stored)
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := r.CreateDataMountBinding(ctx, binding); err != nil {
				return nil, err
			}
			result = append(result, binding)
		default:
			return nil, fmt.Errorf("look up shared data binding: %w", err)
		}
	}
	return result, nil
}

// EnsureTenantRootDataBinding installs the one internal writable FSX adapter
// used to stage the platform TOS root. It is deliberately separate from the
// user-visible team/public bindings: callers use it only to render confined
// subPath mounts, never as a selectable data space.
func (r *GormRepository) EnsureTenantRootDataBinding(ctx context.Context, requested domain.DataMountBinding) (domain.DataMountBinding, error) {
	if requested.Scope != domain.DataMountScopeTenant || requested.SpaceID != domain.DataSpaceTenantStorageRoot || requested.ReadOnly {
		return domain.DataMountBinding{}, fmt.Errorf("tenant storage root must be a writable tenant-scoped binding")
	}
	if err := requested.Validate(); err != nil {
		return domain.DataMountBinding{}, err
	}
	var existing DataMountBindingRecord
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND scope = ? AND space_id = ?", requested.TenantID, domain.DataMountScopeTenant, domain.DataSpaceTenantStorageRoot).
		First(&existing).Error
	switch {
	case err == nil:
		return existing.toDomain()
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return domain.DataMountBinding{}, fmt.Errorf("look up tenant storage root binding: %w", err)
	}
	if err := r.CreateDataMountBinding(ctx, requested); err != nil {
		var duplicate DataMountBindingRecord
		lookupErr := r.db.WithContext(ctx).
			Where("tenant_id = ? AND scope = ? AND space_id = ?", requested.TenantID, domain.DataMountScopeTenant, domain.DataSpaceTenantStorageRoot).
			First(&duplicate).Error
		if lookupErr == nil {
			return duplicate.toDomain()
		}
		return domain.DataMountBinding{}, err
	}
	return requested, nil
}

// EnsureIDCDataBindings installs the tenant-local records for the fixed IDC
// read-only exports. The actual NFS endpoint remains deployment configuration
// and is never stored in this inventory, so database access cannot be used to
// discover or alter an internal export.
func (r *GormRepository) EnsureIDCDataBindings(ctx context.Context, requested ...domain.DataMountBinding) ([]domain.DataMountBinding, error) {
	if len(requested) != 3 {
		return nil, fmt.Errorf("exactly original, wellspiking, and shared IDC bindings are required")
	}
	wanted := map[domain.DataSpaceID]bool{
		domain.DataSpaceIDCOriginal: true, domain.DataSpaceIDCWellspiking: true, domain.DataSpaceIDCShared: true,
	}
	result := make([]domain.DataMountBinding, 0, len(requested))
	for _, binding := range requested {
		if binding.Scope != domain.DataMountScopeIDC || !wanted[binding.SpaceID] {
			return nil, fmt.Errorf("only configured IDC bindings are supported")
		}
		delete(wanted, binding.SpaceID)
		if err := binding.Validate(); err != nil {
			return nil, err
		}
		var existing DataMountBindingRecord
		err := r.db.WithContext(ctx).
			Where("tenant_id = ? AND scope = ? AND space_id = ?", binding.TenantID, domain.DataMountScopeIDC, binding.SpaceID).
			First(&existing).Error
		switch {
		case err == nil:
			stored, conversionErr := existing.toDomain()
			if conversionErr != nil {
				return nil, conversionErr
			}
			result = append(result, stored)
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := r.CreateDataMountBinding(ctx, binding); err != nil {
				return nil, err
			}
			result = append(result, binding)
		default:
			return nil, fmt.Errorf("look up IDC data binding: %w", err)
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("all configured IDC bindings are required")
	}
	return result, nil
}

func (r *GormRepository) CreateDataMountBinding(ctx context.Context, binding domain.DataMountBinding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	record := dataMountBindingRecordFromDomain(binding, now)
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create data mount binding: %w", err)
	}
	return nil
}

// EnsurePersonalDataBinding is idempotent per tenant/user. It is safe to call
// before CSI IRSA exists: the resulting PENDING row stops the UI from claiming
// that a mount is usable and gives infrastructure automation a durable target.
func (r *GormRepository) EnsurePersonalDataBinding(ctx context.Context, requested domain.DataMountBinding) (domain.DataMountBinding, error) {
	if requested.Scope != domain.DataMountScopePersonal {
		return domain.DataMountBinding{}, fmt.Errorf("personal data binding must use personal scope")
	}
	if err := requested.Validate(); err != nil {
		return domain.DataMountBinding{}, err
	}
	var existing DataMountBindingRecord
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND scope = ? AND space_id = ?", requested.TenantID, requested.UserID, domain.DataMountScopePersonal, domain.DataSpaceWorkspace).
		First(&existing).Error
	switch {
	case err == nil:
		if existing.Status == string(domain.DataMountBindingPending) && existing.ClaimName == "" && existing.Driver == "" && existing.VolumeAttributesJSON == "" && existing.RootPrefix == "" {
			existing.ClaimName = requested.ClaimName
			existing.ServiceAccountName = requested.ServiceAccountName
			existing.Driver = requested.Driver
			existing.VolumeAttributesJSON = requested.VolumeAttributesJSON
			existing.RootPrefix = requested.RootPrefix
			existing.ReadOnly = requested.ReadOnly
			existing.UpdatedAt = time.Now().UTC()
			if updateErr := r.db.WithContext(ctx).Save(&existing).Error; updateErr != nil {
				return domain.DataMountBinding{}, fmt.Errorf("complete pending personal data binding: %w", updateErr)
			}
		}
		return existing.toDomain()
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return domain.DataMountBinding{}, fmt.Errorf("look up personal data binding: %w", err)
	}
	if err := r.CreateDataMountBinding(ctx, requested); err != nil {
		// Another request may have inserted the unique personal scope first.
		var duplicate DataMountBindingRecord
		lookupErr := r.db.WithContext(ctx).
			Where("tenant_id = ? AND user_id = ? AND scope = ? AND space_id = ?", requested.TenantID, requested.UserID, domain.DataMountScopePersonal, domain.DataSpaceWorkspace).
			First(&duplicate).Error
		if lookupErr == nil {
			return duplicate.toDomain()
		}
		return domain.DataMountBinding{}, err
	}
	return requested, nil
}

// ListDataBindings applies the exact visibility contract in SQL. Private rows
// are visible only to their owner; tenant/IDC rows only in the same tenant.
// Public TOS is represented by a tenant-local read-only PVC adapter so every
// workload namespace has a concrete claim to mount.
func (r *GormRepository) ListDataBindings(ctx context.Context, tenantID, userID string) ([]domain.DataMountBinding, error) {
	var records []DataMountBindingRecord
	query := r.db.WithContext(ctx).Where(`
        (scope = ? AND tenant_id = ? AND user_id = ?) OR
		(scope IN (?, ?) AND tenant_id = ? AND user_id IS NULL)`,
		domain.DataMountScopePersonal, tenantID, userID,
		domain.DataMountScopeTenant, domain.DataMountScopeIDC, tenantID,
	)
	if err := query.Order("scope ASC, created_at ASC, id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list data mount bindings: %w", err)
	}
	bindings := make([]domain.DataMountBinding, 0, len(records))
	for _, record := range records {
		binding, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (r *GormRepository) GetDataBinding(ctx context.Context, tenantID, userID, id string) (domain.DataMountBinding, error) {
	var record DataMountBindingRecord
	err := r.db.WithContext(ctx).Where("id = ?", id).Where(`
        (scope = ? AND tenant_id = ? AND user_id = ?) OR
		(scope IN (?, ?) AND tenant_id = ? AND user_id IS NULL)`,
		domain.DataMountScopePersonal, tenantID, userID,
		domain.DataMountScopeTenant, domain.DataMountScopeIDC, tenantID,
	).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.DataMountBinding{}, ErrDataMountBindingNotFound
	}
	if err != nil {
		return domain.DataMountBinding{}, fmt.Errorf("get data mount binding: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) UpdateDataBindingStatus(ctx context.Context, id string, status domain.DataMountBindingStatus) error {
	if err := validateDataBindingStatus(status); err != nil {
		return err
	}
	var record DataMountBindingRecord
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&record).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrDataMountBindingNotFound
	} else if err != nil {
		return fmt.Errorf("get data mount binding for update: %w", err)
	}
	record.Status = string(status)
	if _, err := record.toDomain(); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&DataMountBindingRecord{}).Where("id = ?", id).Updates(map[string]any{
		"status": string(status), "updated_at": time.Now().UTC(),
	})
	if result.Error != nil {
		return fmt.Errorf("update data mount binding status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrDataMountBindingNotFound
	}
	return nil
}

func dataMountBindingRecordFromDomain(binding domain.DataMountBinding, now time.Time) DataMountBindingRecord {
	return DataMountBindingRecord{
		ID: binding.ID, TenantID: optionalID(binding.TenantID), UserID: optionalID(binding.UserID), StorageKey: binding.StorageKey, Scope: string(binding.Scope), SpaceID: string(binding.SpaceID),
		ClaimName: binding.ClaimName, ServiceAccountName: binding.ServiceAccountName, Driver: binding.Driver,
		VolumeAttributesJSON: binding.VolumeAttributesJSON, RootPrefix: binding.RootPrefix, ReadOnly: binding.ReadOnly,
		Status: string(binding.Status), CreatedAt: now, UpdatedAt: now,
	}
}

func (record DataMountBindingRecord) toDomain() (domain.DataMountBinding, error) {
	binding := domain.DataMountBinding{
		ID: record.ID, TenantID: valueOrEmpty(record.TenantID), UserID: valueOrEmpty(record.UserID), StorageKey: record.StorageKey,
		Scope: domain.DataMountScope(record.Scope), SpaceID: domain.DataSpaceID(record.SpaceID), ClaimName: record.ClaimName,
		ServiceAccountName: record.ServiceAccountName, Driver: record.Driver,
		VolumeAttributesJSON: record.VolumeAttributesJSON, RootPrefix: record.RootPrefix,
		ReadOnly: record.ReadOnly, Status: domain.DataMountBindingStatus(record.Status),
	}
	if err := binding.Validate(); err != nil {
		return domain.DataMountBinding{}, fmt.Errorf("invalid stored data mount binding: %w", err)
	}
	return binding, nil
}

func validateDataBindingStatus(status domain.DataMountBindingStatus) error {
	switch status {
	case domain.DataMountBindingPending, domain.DataMountBindingReady, domain.DataMountBindingFailed:
		return nil
	default:
		return fmt.Errorf("unsupported data mount binding status %q", status)
	}
}
