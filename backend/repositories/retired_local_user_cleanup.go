package repositories

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

// FindRetiredLocalUserByID is for administrative deletion only, never login.
func (r *GormRepository) FindRetiredLocalUserByID(ctx context.Context, id string) (domain.LocalUser, error) {
	var record LocalUserRecord
	err := r.db.WithContext(ctx).Model(&LocalUserRecord{}).Select("local_users.*").
		Joins("JOIN tenants ON tenants.id = local_users.tenant_id").
		Where("local_users.id = ? AND local_users.decommissioned_at IS NULL AND tenants.retired_at IS NOT NULL", id).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LocalUser{}, ErrLocalUserNotFound
	}
	if err != nil {
		return domain.LocalUser{}, err
	}
	return r.toLocalUser(record)
}

// DecommissionRetiredLocalUser is a monotonic, retention-preserving operation.
// Retirement already revoked sessions/PATs, and authentication rejects retired
// tenants independently. Do not rewrite those credentials or any user data.
func (r *GormRepository) DecommissionRetiredLocalUser(ctx context.Context, id string, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scoped := NewGormRepository(tx)
		user, err := scoped.FindRetiredLocalUserByID(ctx, id)
		if err != nil {
			return err
		}
		if err := lockIdentityTenantFence(tx, user.TenantID); err != nil {
			return err
		}
		var tenant TenantRecord
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&tenant, "id = ? AND retired_at IS NOT NULL", user.TenantID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalUserNotFound
			}
			return err
		}
		var locked LocalUserRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ? AND tenant_id = ? AND decommissioned_at IS NULL", id, user.TenantID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalUserNotFound
			}
			return err
		}
		return scoped.DecommissionLocalUser(ctx, id, now)
	})
}
