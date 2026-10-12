package repositories

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

// Authorization follows live membership; users.tenant_id remains the stable
// historical identity tenant. The user row still serializes quota admission.
func lockSourceArtifactOwner(tx *gorm.DB, tenantID, userID string) error {
	if err := lockIdentityTenantFence(tx, tenantID); err != nil {
		return fmt.Errorf("lock source artifact tenant: %w", err)
	}
	if err := requireActiveIdentityTenant(tx, tenantID, true); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrTenantRetirementBlocked) {
			return ErrSourceArtifactNotFound
		}
		return fmt.Errorf("load source artifact tenant: %w", err)
	}
	legacyOwner := true
	if tx.Migrator().HasTable(&LocalUserRecord{}) {
		var account LocalUserRecord
		err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ?", userID).First(&account).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock source artifact account: %w", err)
		}
		if err == nil {
			if account.Disabled || account.DecommissionedAt != nil {
				return ErrSourceArtifactNotFound
			}
			if err := requireSourceArtifactMembership(tx, account, tenantID); err != nil {
				return err
			}
			legacyOwner = false
		}
	}
	query := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", userID)
	if legacyOwner {
		query = query.Where("tenant_id = ?", tenantID)
	}
	var owner UserRecord
	if err := query.First(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSourceArtifactNotFound
		}
		return fmt.Errorf("lock source artifact owner: %w", err)
	}
	return nil
}

func requireSourceArtifactMembership(tx *gorm.DB, account LocalUserRecord, tenantID string) error {
	if !tx.Migrator().HasTable(&TenantMembershipRecord{}) {
		if account.TenantID != tenantID {
			return ErrSourceArtifactNotFound
		}
		return nil
	}
	// SHARE matches interactive team switching and holds authorization until
	// quota admission commits, without upgrading the membership row lock.
	var membership TenantMembershipRecord
	err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "SHARE"}).
		Where("identity_id = ? AND tenant_id = ? AND status = ?", account.ID, tenantID, domain.MembershipStatusActive).
		First(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrSourceArtifactNotFound
	}
	if err != nil {
		return fmt.Errorf("lock source artifact membership: %w", err)
	}
	return nil
}
