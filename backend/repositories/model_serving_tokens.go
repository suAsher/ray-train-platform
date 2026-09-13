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

// Credentials are task-scoped event tokens, never user PATs. Credentials, job,
// then deployment are locked in the same order as the job event writer.
func (s *ModelServingRepository) AuthorizeServingJobToken(ctx context.Context, jobID string, token []byte, now time.Time) (ms.Deployment, error) {
	if jobID == "" || len(token) != 32 {
		return ms.Deployment{}, ms.ErrUnauthorized
	}
	var d ms.Deployment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var credential TrainingJobEventTokenRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).First(&credential).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ms.ErrUnauthorized
			}
			return err
		}
		if !validTrainingEventToken(credential.TokenSHA256, token) || !now.Before(credential.ExpiresAt) {
			return ms.ErrUnauthorized
		}
		var job JobRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ms.ErrUnauthorized
			}
			return err
		}
		if evaluationJobTerminal(job.ObservedState) || job.DesiredState != string(domain.DesiredActive) {
			return ms.ErrUnauthorized
		}
		var r modelServingDeploymentRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ms.ErrUnauthorized
			}
			return err
		}
		if servingJobMatches(r, job) != nil || ms.Terminal(r.State) || r.State == ms.Stopping || !now.Before(r.ExpiresAt) {
			return ms.ErrUnauthorized
		}
		var err error
		d, err = r.deployment()
		return err
	})
	return d, err
}
