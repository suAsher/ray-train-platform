package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelrelease"
	ms "ray-train-platform-backend/modelserving"
	"reflect"
	"time"
)

func (s *ModelServingRepository) ReserveDeployment(ctx context.Context, d ms.Deployment) (ms.Deployment, bool, error) {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.CreatedAt, d.ExpiresAt = d.CreatedAt.UTC().Truncate(time.Microsecond), d.ExpiresAt.UTC().Truncate(time.Microsecond)
	d.UpdatedAt, d.State, d.Error, d.Revision = now, ms.Creating, "", 1
	if err := ms.ValidateDeployment(d); err != nil {
		return ms.Deployment{}, false, err
	}
	if !d.ExpiresAt.After(now) || d.CreatedAt.After(now.Add(time.Minute)) {
		return ms.Deployment{}, false, ms.ErrInvalid
	}
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "model-serving-owner:"+d.OwnerID+":"+d.ModelID).Error; err != nil {
				return err
			}
		}
		var previous modelServingDeploymentRecord
		err := tx.Where("owner_id = ? AND tenant_id = ? AND idempotency_key = ?", d.OwnerID, d.TenantID, d.IdempotencyKey).First(&previous).Error
		if err == nil {
			if previous.RequestSHA256 != d.RequestSHA256 {
				return ms.ErrConflict
			}
			d, err = previous.deployment()
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Model(&modelServingDeploymentRecord{}).Where("model_id = ? AND owner_id = ? AND state IN ?", d.ModelID, d.OwnerID, []string{ms.Creating, ms.Submitted, ms.Ready, ms.Stopping}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ms.ErrQuota
		}
		if err := validateServingSources(tx, d); err != nil {
			return err
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		spec, err := json.Marshal(d.JobSpec)
		if err != nil {
			return err
		}
		r := modelServingDeploymentRecord{ID: d.ID, Name: d.Name, ReleaseID: d.ReleaseID, ModelID: d.ModelID, VersionID: d.VersionID, ContractID: d.Contract.ID, OwnerID: d.OwnerID, TenantID: d.TenantID, JobID: d.JobID, SnapshotJSON: string(raw), JobSpecJSON: string(spec), State: d.State, Revision: 1, IdempotencyKey: d.IdempotencyKey, RequestSHA256: d.RequestSHA256, ExpiresAt: d.ExpiresAt, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ms.ErrConflict
		}
		if err := writeServingAudit(tx, d.ID, d.OwnerID, "serving.reserved", now); err != nil {
			return err
		}
		created = true
		return nil
	})
	return d, created && err == nil, err
}
func validateServingSources(tx *gorm.DB, d ms.Deployment) error {
	var model ml.Model
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND archived = ?", d.ModelID, false).First(&model).Error; err != nil {
		return servingReadError(err)
	}
	if model.OwnerID != d.OwnerID {
		var identity LocalUserRecord
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND disabled = ? AND decommissioned_at IS NULL", d.OwnerID, false).First(&identity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ms.ErrUnauthorized
			}
			return err
		}
		var roles []string
		if json.Unmarshal([]byte(identity.GlobalRolesJSON), &roles) != nil {
			return ms.ErrUnauthorized
		}
		allowed := false
		for _, role := range roles {
			if role == domain.RoleSuperAdmin {
				allowed = true
			}
		}
		if !allowed {
			return ms.ErrUnauthorized
		}
	}
	var version ml.Version
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND model_id = ?", d.VersionID, d.ModelID).First(&version).Error; err != nil {
		return servingReadError(err)
	}
	if version.State != ml.Ready || version.SHA256 != d.ModelSHA256 || version.SizeBytes != d.ModelSizeBytes || version.FileName != d.FileName {
		return ms.ErrNotReady
	}
	var release mr.Release
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND model_id = ? AND version_id = ?", d.ReleaseID, d.ModelID, d.VersionID).First(&release).Error; err != nil {
		return servingReadError(err)
	}
	if release.State != mr.Approved || release.ModelSHA256 != d.ModelSHA256 || release.ModelOwnerID != model.OwnerID {
		return ms.ErrNotReady
	}
	var record modelServingContractRecord
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", d.Contract.ID).First(&record).Error; err != nil {
		return servingReadError(err)
	}
	current, err := record.contract()
	if err != nil {
		return err
	}
	if !current.Active {
		return ms.ErrNotReady
	}
	current.CreatedAt = current.CreatedAt.UTC()
	frozen := d.Contract
	frozen.CreatedAt = frozen.CreatedAt.UTC()
	// Compare JSON semantically: PostgreSQL JSONB can reorder the example keys.
	left, err := json.Marshal(current)
	if err != nil {
		return err
	}
	right, err := json.Marshal(frozen)
	if err != nil {
		return err
	}
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil || !reflect.DeepEqual(a, b) {
		return ms.ErrConflict
	}
	return nil
}
func lockServingSubmission(tx *gorm.DB, id string) error {
	if id == "" {
		return ms.ErrInvalid
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "model-serving-submit:"+id).Error
}

// The jobs repository calls this in its creation transaction while holding
// lockServingSubmission. Stopping a reservation fences all late submissions.
func validateServingSubmissionReservation(tx *gorm.DB, job *domain.TrainingJob) error {
	if job == nil || job.SubmissionOrigin != domain.SubmissionOriginServing {
		return ms.ErrInvalid
	}
	var r modelServingDeploymentRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", job.ExternalSubmissionID).First(&r).Error; err != nil {
		return servingReadError(err)
	}
	if r.JobID != job.ID || r.OwnerID != job.UserID || r.TenantID != job.TenantID {
		return ms.ErrConflict
	}
	var existing JobRecord
	err := tx.Where("id = ?", job.ID).First(&existing).Error
	if err == nil {
		if servingJobMatches(r, existing) != nil {
			return ms.ErrConflict
		}
		return &IdempotencyConflictError{JobID: existing.ID}
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if r.State != ms.Creating || !time.Now().Before(r.ExpiresAt) {
		return ms.ErrConflict
	}
	d, err := r.deployment()
	if err != nil {
		return err
	}
	// SubmissionService fills trusted queue, mount and runtime defaults. Compare
	// caller-selectable frozen execution fields, not those resolved defaults.
	spec := job.Spec
	frozen := d.JobSpec
	if spec.Source != frozen.Source || spec.Image != frozen.Image || !reflect.DeepEqual(spec.Entrypoint, frozen.Entrypoint) || spec.Resources != frozen.Resources || spec.TimeoutSeconds != frozen.TimeoutSeconds || spec.Name != frozen.Name || !reflect.DeepEqual(spec.Output, frozen.Output) || !spec.DatasetRef.IsZero() || spec.DatasetURI != "" || spec.DataMode != frozen.DataMode || !reflect.DeepEqual(spec.Input, frozen.Input) {
		return ms.ErrConflict
	}
	return nil
}
func servingJobMatches(r modelServingDeploymentRecord, job JobRecord) error {
	if r.JobID != job.ID || r.OwnerID != job.UserID || r.TenantID != job.TenantID || job.TrainingEngine != string(domain.TrainingEngineRayTrain) || job.SubmissionOrigin != string(domain.SubmissionOriginServing) || job.ExternalSubmissionID != r.ID {
		return ms.ErrConflict
	}
	return nil
}
