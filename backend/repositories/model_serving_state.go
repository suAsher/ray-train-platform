package repositories

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"time"
)

// Always lock the job before the deployment, matching token authorization and
// the normal job event writer. A submission advisory lock also fences no-job
// reservations, for which PostgreSQL has no job row to lock yet.
func loadServingForUpdate(tx *gorm.DB, id string) (modelServingDeploymentRecord, *JobRecord, error) {
	var r modelServingDeploymentRecord
	if err := lockServingSubmission(tx, id); err != nil {
		return r, nil, err
	}
	if err := tx.Where("id = ?", id).First(&r).Error; err != nil {
		return r, nil, servingReadError(err)
	}
	var job JobRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", r.JobID).First(&job).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return r, nil, err
	}
	exists := err == nil
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&r).Error; err != nil {
		return r, nil, err
	}
	if exists {
		if err := servingJobMatches(r, job); err != nil {
			return r, nil, err
		}
		return r, &job, nil
	}
	return r, nil, nil
}
func (s *ModelServingRepository) MarkDeploymentSubmitted(ctx context.Context, id, jobID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, job, err := loadServingForUpdate(tx, id)
		if err != nil {
			return err
		}
		if r.JobID != jobID || job == nil {
			return ms.ErrConflict
		}
		if r.State == ms.Submitted || r.State == ms.Ready {
			return nil
		}
		if r.State != ms.Creating {
			return ms.ErrConflict
		}
		return updateServingState(tx, r, ms.Submitted, r.OwnerID, time.Now().UTC())
	})
}
func (s *ModelServingRepository) FailDeploymentSubmission(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, job, err := loadServingForUpdate(tx, id)
		if err != nil {
			return err
		}
		if ms.Terminal(r.State) {
			return nil
		}
		if job != nil || r.State != ms.Creating {
			return ms.ErrConflict
		}
		return updateServingState(tx, r, ms.Failed, r.OwnerID, time.Now().UTC())
	})
}
func (s *ModelServingRepository) RequestStop(ctx context.Context, id, actor string, revision int64) (ms.Deployment, error) {
	if actor == "" || revision < 1 {
		return ms.Deployment{}, ms.ErrInvalid
	}
	var d ms.Deployment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, job, err := loadServingForUpdate(tx, id)
		if err != nil {
			return err
		}
		if r.Revision != revision {
			return ms.ErrConflict
		}
		if ms.Terminal(r.State) {
			d, err = r.deployment()
			return err
		}
		state := ms.Stopping
		if job == nil {
			if r.State != ms.Creating && r.State != ms.Stopping {
				return ms.ErrConflict
			}
			state = ms.Stopped
		}
		if job != nil && evaluationJobTerminal(job.ObservedState) {
			state = ms.Stopped
		}
		if err := updateServingState(tx, r, state, actor, time.Now().UTC()); err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).First(&r).Error; err != nil {
			return err
		}
		d, err = r.deployment()
		return err
	})
	return d, err
}
func (s *ModelServingRepository) UpdateObserved(ctx context.Context, id, state string, revision int64) (ms.Deployment, error) {
	if revision < 1 {
		return ms.Deployment{}, ms.ErrInvalid
	}
	switch state {
	case ms.Submitted, ms.Ready, ms.Stopping, ms.Stopped, ms.Failed, ms.Expired:
	default:
		return ms.Deployment{}, ms.ErrInvalid
	}
	var d ms.Deployment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, job, err := loadServingForUpdate(tx, id)
		if err != nil {
			return err
		}
		if r.Revision != revision {
			return ms.ErrConflict
		}
		if ms.Terminal(r.State) {
			if r.State != state {
				return ms.ErrConflict
			}
			d, err = r.deployment()
			return err
		}
		now := time.Now().UTC()
		if r.State == ms.Stopping && !ms.Terminal(state) && state != ms.Stopping {
			return ms.ErrConflict
		}
		if ms.Terminal(state) {
			// A health failure never releases the active reservation while a compute
			// job still exists and is nonterminal. Stop it through normal cancellation.
			if job != nil && !evaluationJobTerminal(job.ObservedState) {
				return ms.ErrConflict
			}
			if job == nil && r.State != ms.Creating && r.State != ms.Stopping {
				return ms.ErrConflict
			}
			if state == ms.Expired && now.Before(r.ExpiresAt) {
				return ms.ErrConflict
			}
		} else if state == ms.Ready {
			if job == nil || job.DesiredState != string(domain.DesiredActive) || job.ObservedState != string(domain.StateRunning) || !now.Before(r.ExpiresAt) {
				return ms.ErrNotReady
			}
		} else if state == ms.Submitted {
			if job == nil || evaluationJobTerminal(job.ObservedState) || job.DesiredState != string(domain.DesiredActive) {
				return ms.ErrConflict
			}
		}
		if err := updateServingState(tx, r, state, "system", now); err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).First(&r).Error; err != nil {
			return err
		}
		d, err = r.deployment()
		return err
	})
	return d, err
}
func updateServingState(tx *gorm.DB, r modelServingDeploymentRecord, state, actor string, now time.Time) error {
	if r.State == state {
		return tx.Model(&r).Update("updated_at", now).Error
	}
	message := ""
	if state == ms.Failed {
		message = "serving job failed; inspect its task status"
	}
	if state == ms.Expired {
		message = "serving lifetime expired"
	}
	if err := tx.Model(&r).Updates(map[string]any{"state": state, "error": message, "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
		return err
	}
	return writeServingAudit(tx, r.ID, actor, "serving.state."+state, now)
}
