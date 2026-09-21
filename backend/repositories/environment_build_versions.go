package repositories

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
	eb "ray-train-platform-backend/environmentbuild"
)

// FinalizeEnvironmentBuild publishes the immutable directory entry and version
// together. A DB rollback leaves VERIFYING_PULL so retry cannot re-push layers.
func (r *GormRepository) FinalizeEnvironmentBuild(ctx context.Context, b eb.Build, leaseOwner string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current eb.Build
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", b.ID).First(&current).Error; err != nil {
			return environmentError(err)
		}
		if current.LeaseOwner != leaseOwner || current.Status != eb.VerifyingPull {
			return eb.ErrConflict
		}
		image := domain.PlatformImage{ID: b.ImageID, TenantID: b.TenantID, OwnerUserID: b.OwnerID, Visibility: b.Visibility, EnvironmentVersionID: b.ID, Name: b.Name, Description: b.Description, Reference: b.ImageReference, Kind: domain.ImageKindTraining, RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP, domain.TrainingEngineRayTrain}, CreatedBy: b.OwnerID}
		if err := image.Validate(); err != nil {
			return err
		}
		engines, _ := json.Marshal(image.SupportedEngines)
		now := time.Now().UTC()
		record := PlatformImageRecord{ID: image.ID, TenantID: optionalID(image.TenantID), OwnerUserID: image.OwnerUserID, Visibility: image.Visibility, EnvironmentVersionID: b.ID, Name: image.Name, Description: image.Description, Reference: image.Reference, Kind: image.Kind, RayVersion: image.RayVersion, SupportedEnginesJSON: string(engines), EnvironmentJSON: "{}", CreatedBy: b.OwnerID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		v := eb.Version{ID: b.ID, BuildID: b.ID, TenantID: b.TenantID, OwnerID: b.OwnerID, Visibility: b.Visibility, Name: b.Name, Description: b.Description, ImageID: b.ImageID, ImageReference: b.ImageReference, BaseImage: b.BaseImage, ChecksJSON: b.ChecksJSON, CreatedAt: now}
		if err := tx.Create(&v).Error; err != nil {
			return err
		}
		b.LeaseOwner = ""
		b.LeaseUntil = nil
		b.UpdatedAt = now
		b.CleanedAt = nil
		return tx.Save(&b).Error
	})
}
