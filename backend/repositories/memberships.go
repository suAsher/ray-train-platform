package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var (
	ErrMembershipNotFound = errors.New("tenant membership not found")
	ErrLastMembership     = errors.New("the last active membership cannot be disabled")
)

type TenantMembershipRecord struct {
	IdentityID string `gorm:"primaryKey;column:identity_id"`
	TenantID   string `gorm:"primaryKey;column:tenant_id;index"`
	RolesJSON  string `gorm:"column:roles;type:jsonb"`
	Status     string `gorm:"column:status;index"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (TenantMembershipRecord) TableName() string { return "tenant_memberships" }

func membershipRoles(encoded string) ([]string, error) {
	var roles []string
	if err := json.Unmarshal([]byte(encoded), &roles); err != nil {
		return nil, fmt.Errorf("decode membership roles: %w", err)
	}
	return domain.NormalizeRoles(roles)
}

func encodeMembershipRoles(roles []string) (string, error) {
	normalized, err := domain.NormalizeRoles(roles)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode membership roles: %w", err)
	}
	return string(encoded), nil
}

func (r *GormRepository) ListTenantMemberships(ctx context.Context, identityID string) ([]domain.TenantMembership, error) {
	var records []TenantMembershipRecord
	if err := r.db.WithContext(ctx).Where("identity_id = ?", strings.TrimSpace(identityID)).Order("created_at ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list tenant memberships: %w", err)
	}
	var account LocalUserRecord
	if err := r.db.WithContext(ctx).Where("id = ? AND decommissioned_at IS NULL", identityID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLocalUserNotFound
		}
		return nil, fmt.Errorf("load membership identity: %w", err)
	}
	items := make([]domain.TenantMembership, 0, len(records))
	for _, record := range records {
		roles, err := membershipRoles(record.RolesJSON)
		if err != nil {
			return nil, err
		}
		var tenant TenantRecord
		if err := r.db.WithContext(ctx).Select("id", "name").Where("id = ?", record.TenantID).First(&tenant).Error; err != nil {
			return nil, fmt.Errorf("load membership tenant: %w", err)
		}
		items = append(items, domain.TenantMembership{
			IdentityID: record.IdentityID, TenantID: record.TenantID, TenantName: tenant.Name,
			Roles: roles, Status: domain.MembershipStatus(record.Status),
			Active: account.activeTenantID() == record.TenantID,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		})
	}
	return items, nil
}

func (r *GormRepository) PutTenantMembership(ctx context.Context, membership domain.TenantMembership) error {
	if err := membership.Validate(); err != nil {
		return err
	}
	roles, err := encodeMembershipRoles(membership.Roles)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return r.withActiveIdentityTenant(ctx, membership.TenantID, func(tx *gorm.DB) error {
		var account LocalUserRecord
		if err := tx.Where("id = ? AND decommissioned_at IS NULL", membership.IdentityID).First(&account).Error; err != nil {
			return ErrLocalUserNotFound
		}
		record := TenantMembershipRecord{
			IdentityID: membership.IdentityID, TenantID: membership.TenantID,
			RolesJSON: roles, Status: string(membership.Status), CreatedAt: now, UpdatedAt: now,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "identity_id"}, {Name: "tenant_id"}},
			DoUpdates: clause.Assignments(map[string]any{"roles": roles, "status": membership.Status, "updated_at": now}),
		}).Create(&record).Error
	})
}

func (r *GormRepository) SetActiveTenant(ctx context.Context, identityID, tenantID string) error {
	identityID, tenantID = strings.TrimSpace(identityID), strings.TrimSpace(tenantID)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockIdentityTenantFence(tx, tenantID); err != nil {
			return err
		}
		if err := requireActiveIdentityTenant(tx, tenantID, true); err != nil {
			return ErrMembershipNotFound
		}
		var membership TenantMembershipRecord
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("identity_id = ? AND tenant_id = ? AND status = ?", identityID, tenantID, domain.MembershipStatusActive).First(&membership).Error; err != nil {
			return ErrMembershipNotFound
		}
		result := tx.Model(&LocalUserRecord{}).Where("id = ? AND disabled = FALSE AND decommissioned_at IS NULL", identityID).
			Updates(map[string]any{"active_tenant_id": tenantID, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return fmt.Errorf("switch active tenant: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrLocalUserNotFound
		}
		return nil
	})
}

func (r *GormRepository) SetTenantMembershipStatus(ctx context.Context, identityID, tenantID string, status domain.MembershipStatus) error {
	if status != domain.MembershipStatusActive && status != domain.MembershipStatusInactive {
		return fmt.Errorf("unsupported membership status %q", status)
	}
	return r.withActiveIdentityTenant(ctx, tenantID, func(tx *gorm.DB) error {
		var account LocalUserRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identityID).First(&account).Error; err != nil {
			return ErrLocalUserNotFound
		}
		if status == domain.MembershipStatusInactive {
			var active int64
			if err := tx.Model(&TenantMembershipRecord{}).Where("identity_id = ? AND status = ?", identityID, domain.MembershipStatusActive).Count(&active).Error; err != nil {
				return err
			}
			if active <= 1 || account.activeTenantID() == tenantID {
				return ErrLastMembership
			}
		}
		result := tx.Model(&TenantMembershipRecord{}).Where("identity_id = ? AND tenant_id = ?", identityID, tenantID).
			Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrMembershipNotFound
		}
		return nil
	})
}

func mergeMembershipRoles(membershipRoles, globalRoles []string) []string {
	combined := append([]string(nil), membershipRoles...)
	for _, role := range globalRoles {
		if role == domain.RoleSuperAdmin {
			combined = append(combined, role)
		}
	}
	normalized, err := domain.NormalizeRoles(combined)
	if err != nil {
		return append([]string(nil), membershipRoles...)
	}
	return normalized
}
