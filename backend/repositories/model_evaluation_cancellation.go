package repositories

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

func lockEvaluationSubmission(tx *gorm.DB, id string) error {
	if id == "" {
		return me.ErrInvalid
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "model-evaluation-submit:"+id).Error
}

// Called inside the training job creation transaction after its idempotency
// lookup. A cancelled reservation can never create a late training job.
func validateEvaluationSubmissionReservation(tx *gorm.DB, job *domain.TrainingJob) error {
	var r modelEvaluationRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", job.ExternalSubmissionID).First(&r).Error; err != nil {
		return evaluationReadError(err)
	}
	if r.JobID != job.ID || r.OwnerID != job.UserID || r.TenantID != job.TenantID {
		return me.ErrConflict
	}
	var existing JobRecord
	err := tx.Where("id = ?", job.ID).First(&existing).Error
	if err == nil {
		if evaluationJobMatches(r, existing) != nil {
			return me.ErrConflict
		}
		return &IdempotencyConflictError{JobID: existing.ID}
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if r.State != me.Creating {
		return me.ErrConflict
	}
	return nil
}

// True means no live job remains to cancel: a jobless reservation was
// cancelled, or the evaluation was already terminal. False delegates to the
// existing training-job cancellation path after the caller authorizes it.
func (s *ModelEvaluationRepository) CancelEvaluationReservation(ctx context.Context, id string) (bool, error) {
	cancelled := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvaluationSubmission(tx, id); err != nil {
			return err
		}
		var r modelEvaluationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&r).Error; err != nil {
			return evaluationReadError(err)
		}
		if evaluationTerminal(r.State) {
			cancelled = true
			return nil
		}
		var count int64
		if err := tx.Model(&JobRecord{}).Where("id = ?", r.JobID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		if r.State != me.Creating {
			return me.ErrConflict
		}
		now := time.Now().UTC()
		if err := tx.Model(&r).Updates(map[string]any{"state": me.Cancelled, "report_state": me.ReportMissing, "error": "evaluation was cancelled before job submission", "revision": gorm.Expr("revision + 1"), "updated_at": now, "finished_at": now}).Error; err != nil {
			return err
		}
		cancelled = true
		return writeEvaluationAudit(tx, r.ID, "evaluation.reservation_cancelled", r.OwnerID, now)
	})
	return cancelled && err == nil, err
}
