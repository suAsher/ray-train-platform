package repositories

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IdentityTenantOwnershipRecord preserves historical resource ownership. It
// is not an authorization record; access is always decided by an ACTIVE
// TenantMembershipRecord.
type IdentityTenantOwnershipRecord struct {
	IdentityID string `gorm:"primaryKey;column:identity_id"`
	TenantID   string `gorm:"primaryKey;column:tenant_id"`
	CreatedAt  time.Time
}

func (IdentityTenantOwnershipRecord) TableName() string { return "identity_tenant_ownerships" }

func ensureIdentityTenantOwnership(tx *gorm.DB, identityID, tenantID string, now time.Time) error {
	if !tx.Migrator().HasTable(&IdentityTenantOwnershipRecord{}) {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "identity_id"}, {Name: "tenant_id"}},
		DoNothing: true,
	}).Create(&IdentityTenantOwnershipRecord{
		IdentityID: identityID,
		TenantID:   tenantID,
		CreatedAt:  now,
	}).Error
}

