package repositories

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	eb "ray-train-platform-backend/environmentbuild"
)

func (r *GormRepository) EnvironmentWorkspace(ctx context.Context, o eb.Owner, id string) (eb.Workspace, error) {
	var w WorkspaceRecord
	if err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND user_id = ?", id, o.TenantID, o.UserID).First(&w).Error; err != nil {
		return eb.Workspace{}, environmentError(err)
	}
	return eb.Workspace{ID: w.ID, TenantID: w.TenantID, OwnerID: w.UserID, Namespace: w.Namespace, ResourceName: w.RayClusterName, State: w.ObservedState}, nil
}
func environmentError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return eb.ErrNotFound
	}
	return err
}
func (r *GormRepository) SaveEnvironmentAuthorization(ctx context.Context, a eb.Authorization) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old eb.Authorization
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", a.ID).First(&old).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if a.BuildID != "" {
				return eb.ErrNotFound
			}
			return tx.Create(&a).Error
		}
		if err != nil {
			return err
		}
		if old.OwnerID != a.OwnerID || old.TenantID != a.TenantID || old.Username != a.Username || (!old.ExpiresAt.Equal(a.ExpiresAt)) || (old.BuildID != "" && old.BuildID != a.BuildID) || (old.Target != "" && old.Target != a.Target) {
			return eb.ErrConflict
		}
		return tx.Save(&a).Error
	})
}
func (r *GormRepository) EnvironmentAuthorization(ctx context.Context, o eb.Owner, id string) (eb.Authorization, error) {
	var a eb.Authorization
	err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND owner_id = ?", id, o.TenantID, o.UserID).First(&a).Error
	return a, environmentError(err)
}
func (r *GormRepository) DeleteEnvironmentAuthorization(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&eb.Authorization{}).Error
}
func (r *GormRepository) ExpiredEnvironmentAuthorizations(ctx context.Context, now time.Time) ([]eb.Authorization, error) {
	items := []eb.Authorization{}
	err := r.db.WithContext(ctx).Where("expires_at <= ?", now).Limit(100).Find(&items).Error
	return items, err
}
func (r *GormRepository) CreateEnvironmentBuild(ctx context.Context, b eb.Build) (eb.Build, error) {
	var found eb.Build
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(2026092155)).Error; err != nil {
				return err
			}
		}
		err := tx.Where("tenant_id = ? AND owner_id = ? AND idempotency_key = ?", b.TenantID, b.OwnerID, b.IdempotencyKey).First(&found).Error
		if err == nil {
			if found.WorkspaceID != b.WorkspaceID || found.Project != b.Project || found.Repository != b.Repository || found.Name != b.Name || found.Description != b.Description || found.Visibility != b.Visibility {
				return eb.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Reserve disk budget even for QUEUED operations. Retriable failed OCI
		// volumes count until their TTL cleanup; historical completed rows do not.
		occupied := func() *gorm.DB {
			return tx.Model(&eb.Build{}).Where("(cleaned_at IS NULL OR (status IN ? AND (artifact_expires_at > ? OR cleaned_at < artifact_expires_at)))", []string{eb.Failed, eb.AwaitingAuth}, time.Now().UTC())
		}
		var global, user int64
		if err = occupied().Count(&global).Error; err != nil {
			return err
		}
		if err = occupied().Where("owner_id = ?", b.OwnerID).Count(&user).Error; err != nil {
			return err
		}
		if global >= 8 || user >= 3 {
			return eb.ErrCapacity
		}
		if err = tx.Create(&b).Error; err != nil {
			return err
		}
		found = b
		return nil
	})
	return found, err
}
func (r *GormRepository) EnvironmentBuild(ctx context.Context, o eb.Owner, id string) (eb.Build, error) {
	var b eb.Build
	err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND owner_id = ?", id, o.TenantID, o.UserID).First(&b).Error
	return b, environmentError(err)
}
func (r *GormRepository) ListEnvironmentBuilds(ctx context.Context, o eb.Owner) ([]eb.Build, error) {
	items := []eb.Build{}
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND owner_id = ?", o.TenantID, o.UserID).Order("created_at DESC").Limit(200).Find(&items).Error
	return items, err
}
func (r *GormRepository) ListEnvironmentVersions(ctx context.Context, o eb.Owner) ([]eb.Version, error) {
	items := []eb.Version{}
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND (owner_id = ? OR visibility = 'team')", o.TenantID, o.UserID).Order("created_at DESC").Limit(200).Find(&items).Error
	return items, err
}

var activeEnvironmentStates = []string{eb.Capturing, eb.Building, eb.Validating, eb.Pushing, eb.VerifyingPull, eb.CancelRequested}

func (r *GormRepository) ClaimEnvironmentBuild(ctx context.Context, controller string, now time.Time, lease time.Duration, globalLimit, userLimit int) (*eb.Build, error) {
	var claimed *eb.Build
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serializes admission across backend replicas, without locking user jobs.
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(2026092155)).Error; err != nil {
				return err
			}
		}
		var candidates []eb.Build
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("(lease_until IS NULL OR lease_until < ?) AND (status IN ? OR (status IN ? AND (cleaned_at IS NULL OR (status IN ? AND artifact_expires_at <= ? AND cleaned_at < artifact_expires_at))))", now, append([]string{eb.Queued}, activeEnvironmentStates...), []string{eb.Ready, eb.Failed, eb.AwaitingAuth, eb.Canceled}, []string{eb.Failed, eb.AwaitingAuth}, now).Order("updated_at ASC").Limit(100).Find(&candidates).Error
		if err != nil {
			return err
		}
		for _, b := range candidates {
			if b.Status == eb.Queued {
				var global, user int64
				if err = tx.Model(&eb.Build{}).Where("status IN ?", activeEnvironmentStates).Count(&global).Error; err != nil {
					return err
				}
				if global >= int64(globalLimit) {
					continue
				}
				if err = tx.Model(&eb.Build{}).Where("status IN ? AND owner_id = ?", activeEnvironmentStates, b.OwnerID).Count(&user).Error; err != nil {
					return err
				}
				if user >= int64(userLimit) {
					continue
				}
				// Admission and transition happen under the same global lock, avoiding a
				// second controller admitting while the first QUEUED lease is outstanding.
				b.Status = eb.Capturing
				if b.ResumeStatus != "" {
					b.Status = b.ResumeStatus
					b.ResumeStatus = ""
				}
			}
			until := now.Add(lease)
			b.LeaseUntil = &until
			b.LeaseOwner = controller
			b.UpdatedAt = now
			if err = tx.Save(&b).Error; err != nil {
				return err
			}
			claimed = &b
			break
		}
		return nil
	})
	return claimed, err
}
func (r *GormRepository) SaveEnvironmentBuild(ctx context.Context, b eb.Build, leaseOwner string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current eb.Build
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", b.ID).First(&current).Error; err != nil {
			return environmentError(err)
		}
		if current.LeaseOwner != leaseOwner {
			return eb.ErrConflict
		}
		// Cancellation wins over stale phase advancement, while retaining evidence
		// of a push that actually completed before the cancellation was observed.
		if current.Status == eb.CancelRequested && b.Status != eb.Canceled {
			b.Status = eb.CancelRequested
			b.CleanedAt = nil
		}
		b.LeaseOwner = ""
		b.LeaseUntil = nil
		b.UpdatedAt = time.Now().UTC()
		return tx.Save(&b).Error
	})
}
func (r *GormRepository) RetryEnvironmentBuild(ctx context.Context, o eb.Owner, id, authID string, now time.Time) (eb.Build, error) {
	var b eb.Build
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND owner_id = ?", id, o.TenantID, o.UserID).First(&b).Error; err != nil {
			return environmentError(err)
		}
		if (b.Status != eb.Failed && b.Status != eb.AwaitingAuth) || b.CleanedAt == nil || b.Attempt >= 5 || !now.Before(b.ArtifactExpiresAt) || (b.LeaseUntil != nil && now.Before(*b.LeaseUntil)) {
			return eb.ErrConflict
		}
		resume := b.ResumeStatus
		if b.ImageDigest != "" {
			resume = eb.VerifyingPull
		} else if b.ArtifactDigest != "" && resume != eb.Validating {
			resume = eb.Pushing
		} else if b.SnapshotJSON != "" {
			resume = eb.Building
		} else {
			resume = eb.Capturing
		}
		// Re-admission still uses the global/user limits. ResumeStatus tells the
		// admission transaction which already-captured phase to continue.
		b.Status = eb.Queued
		b.ResumeStatus = resume
		b.AuthID = authID
		b.Attempt++
		b.CleanedAt = nil
		b.Message = "等待重试"
		b.UpdatedAt = now
		b.LeaseOwner = ""
		b.LeaseUntil = nil
		return tx.Save(&b).Error
	})
	return b, err
}
func (r *GormRepository) CancelEnvironmentBuild(ctx context.Context, o eb.Owner, id string) (eb.Build, error) {
	var b eb.Build
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND owner_id = ?", id, o.TenantID, o.UserID).First(&b).Error; err != nil {
			return environmentError(err)
		}
		if b.Status == eb.Ready || b.Status == eb.Canceled {
			return eb.ErrConflict
		}
		b.Status = eb.CancelRequested
		b.CleanedAt = nil
		b.UpdatedAt = time.Now().UTC()
		return tx.Save(&b).Error
	})
	return b, err
}

func (r *GormRepository) ReserveEnvironmentCredentialMaterial(ctx context.Context, m eb.CredentialMaterial) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(2026092156)).Error; err != nil {
				return err
			}
		}
		// Count authorizations and preallocated refs as one identity. Expired refs
		// still occupy capacity until their actual Secret cleanup completes.
		const reserved = "(SELECT authorization_id AS id, owner_id FROM environment_credential_materials UNION SELECT id, owner_id FROM environment_registry_authorizations) AS credential_reservations"
		var existing int64
		if err := tx.Table(reserved).Where("id = ? AND owner_id = ?", m.AuthorizationID, m.OwnerID).Count(&existing).Error; err != nil {
			return err
		}
		if existing == 0 {
			var global, user int64
			if err := tx.Table(reserved).Count(&global).Error; err != nil {
				return err
			}
			if err := tx.Table(reserved).Where("owner_id = ?", m.OwnerID).Count(&user).Error; err != nil {
				return err
			}
			if global >= 50 || user >= 5 {
				return eb.ErrCredentialCapacity
			}
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&m)
		if result.Error != nil {
			return result.Error
		}
		var stored eb.CredentialMaterial
		if err := tx.Where("ref = ?", m.Ref).First(&stored).Error; err != nil {
			return err
		}
		if stored.AuthorizationID != m.AuthorizationID || stored.OwnerID != m.OwnerID || stored.TenantID != m.TenantID || !stored.ExpiresAt.Equal(m.ExpiresAt) {
			return eb.ErrConflict
		}
		return nil
	})
}
func (r *GormRepository) EnvironmentCredentialMaterials(ctx context.Context, id string) ([]eb.CredentialMaterial, error) {
	items := []eb.CredentialMaterial{}
	err := r.db.WithContext(ctx).Where("authorization_id = ?", id).Find(&items).Error
	return items, err
}
func (r *GormRepository) ExpiredEnvironmentCredentialMaterials(ctx context.Context, now time.Time) ([]eb.CredentialMaterial, error) {
	items := []eb.CredentialMaterial{}
	err := r.db.WithContext(ctx).Where("expires_at <= ?", now).Limit(100).Find(&items).Error
	return items, err
}
func (r *GormRepository) DeleteEnvironmentCredentialMaterial(ctx context.Context, ref string) error {
	return r.db.WithContext(ctx).Where("ref = ?", ref).Delete(&eb.CredentialMaterial{}).Error
}
