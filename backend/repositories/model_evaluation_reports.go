package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

func evaluationJobTerminal(state string) bool {
	switch domain.State(state) {
	case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
		return true
	default:
		return false
	}
}

func evaluationTerminal(state string) bool {
	return state == me.Succeeded || state == me.Failed || state == me.Cancelled
}

// Lock credentials, then the job, then the evaluation. This agrees with the
// training event writer and prevents a report racing a terminal job update.
func authorizeEvaluationJob(tx *gorm.DB, jobID string, token []byte, now time.Time) (modelEvaluationRecord, TrainingJobEventTokenRecord, error) {
	var r modelEvaluationRecord
	var credential TrainingJobEventTokenRecord
	if jobID == "" || len(token) != 32 { return r, credential, me.ErrUnauthorized }
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).First(&credential).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return r, credential, me.ErrUnauthorized }
		return r, credential, err
	}
	if !validTrainingEventToken(credential.TokenSHA256, token) || !now.Before(credential.ExpiresAt) { return r, credential, me.ErrUnauthorized }
	var job JobRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return r, credential, me.ErrUnauthorized }
		return r, credential, err
	}
	if evaluationJobTerminal(job.ObservedState) || job.DesiredState == string(domain.DesiredCanceled) { return r, credential, me.ErrConflict }
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return r, credential, me.ErrUnauthorized }
		return r, credential, err
	}
	if evaluationJobMatches(r, job) != nil { return r, credential, me.ErrUnauthorized }
	if evaluationTerminal(r.State) { return r, credential, me.ErrConflict }
	return r, credential, nil
}

func (s *ModelEvaluationRepository) AuthorizeEvaluationJobToken(ctx context.Context, jobID string, token []byte, now time.Time) (me.Evaluation, error) {
	var e me.Evaluation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, _, err := authorizeEvaluationJob(tx, jobID, token, now.UTC())
		if err != nil { return err }
		e, err = r.evaluation()
		return err
	})
	return e, err
}

func (s *ModelEvaluationRepository) StoreEvaluationReport(ctx context.Context, jobID string, token, raw []byte, now time.Time) (me.Evaluation, error) {
	var e me.Evaluation
	var validationErr error
	now = now.UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, credential, err := authorizeEvaluationJob(tx, jobID, token, now)
		if err != nil { return err }
		if err := consumeTrainingEventRate(&credential, now); err != nil { return err }
		credential.UpdatedAt = now
		if err := tx.Save(&credential).Error; err != nil { return err }
		e, err = r.evaluation()
		if err != nil { return err }
		report, digest, err := me.ValidateReport(raw, e)
		if err != nil {
			validationErr = me.ErrInvalid
			if r.ReportState == me.ReportValid { return nil }
			// Retain only the validation outcome, never malformed report content.
			return tx.Model(&r).Updates(map[string]any{"report_state": me.ReportInvalid, "error": "evaluation report validation failed", "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error
		}
		if r.ReportSHA256 != "" {
			if r.ReportSHA256 != digest { return me.ErrConflict }
			return nil
		}
		canonical, err := json.Marshal(report)
		if err != nil { return me.ErrInvalid }
		if err := tx.Model(&r).Updates(map[string]any{"report_json": string(canonical), "report_sha256": digest, "report_state": me.ReportValid, "error": "", "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil { return err }
		if err := writeEvaluationAudit(tx, r.ID, "evaluation.report_received", r.OwnerID, now); err != nil { return err }
		if err := tx.Where("id = ?", r.ID).First(&r).Error; err != nil { return err }
		e, err = r.evaluation()
		return err
	})
	if err != nil { return me.Evaluation{}, err }
	if validationErr != nil { return me.Evaluation{}, validationErr }
	return e, nil
}

// FinalizeEvaluationJob reads the committed training job state. A stale
// reconciler argument cannot finalize a still-running or unrelated job.
func (s *ModelEvaluationRepository) FinalizeEvaluationJob(ctx context.Context, input *domain.TrainingJob) error {
	if input == nil || input.ID == "" { return me.ErrInvalid }
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job JobRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", input.ID).First(&job).Error; err != nil { return evaluationReadError(err) }
		var r modelEvaluationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", input.ID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) { return nil }
			return err
		}
		if err := evaluationJobMatches(r, job); err != nil { return err }
		if evaluationTerminal(r.State) { return nil }
		state, reportState, message := r.State, r.ReportState, r.Error
		var finished *time.Time
		now := time.Now().UTC()
		if !evaluationJobTerminal(job.ObservedState) {
			if job.ObservedState != string(domain.StateRunning) || r.State == me.Running { return nil }
			state = me.Running
		} else {
			finished = &now
			state = me.Failed
			message = "evaluation job did not succeed"
			if reportState == me.ReportPending { reportState = me.ReportMissing }
			if job.ObservedState == string(domain.StateCanceled) {
				state, message = me.Cancelled, "evaluation job was cancelled"
			} else if job.ObservedState == string(domain.StateSucceeded) {
				message = "evaluation report is missing or invalid"
				if r.ReportState == me.ReportValid {
					e, err := r.evaluation()
					if err != nil { return err }
					_, digest, err := me.ValidateReport([]byte(r.ReportJSON), e)
					if err == nil && digest == r.ReportSHA256 { state, message = me.Succeeded, "" } else { reportState = me.ReportInvalid }
				}
			}
		}
		if err := tx.Model(&r).Updates(map[string]any{"state": state, "report_state": reportState, "error": message, "revision": gorm.Expr("revision + 1"), "updated_at": now, "finished_at": finished}).Error; err != nil { return err }
		return writeEvaluationAudit(tx, r.ID, "evaluation.state." + state, r.OwnerID, now)
	})
}
