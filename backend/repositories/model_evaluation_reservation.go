package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
)

const maxActiveModelEvaluations = 16

func (s *ModelEvaluationRepository) ReserveEvaluation(ctx context.Context, e me.Evaluation) (me.Evaluation, bool, error) {
	if e.ID == "" { e.ID = uuid.NewString() }
	if e.JobID == "" { return me.Evaluation{}, false, me.ErrInvalid }
	e.State, e.ReportState, e.Revision = me.Creating, me.ReportPending, 1
	e.Report, e.ReportSHA256, e.Error, e.FinishedAt = nil, "", "", nil
	e.CreatedAt, e.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := me.ValidateEvaluation(e); err != nil { return me.Evaluation{}, false, err }
	if e.OwnerID == "" || e.TenantID == "" || len(e.IdempotencyKey) < 1 || len(e.IdempotencyKey) > 128 || !validEvaluationDigest(e.RequestSHA256) { return me.Evaluation{}, false, me.ErrInvalid }
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Stable-owner serialization covers requests across the owner's teams.
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "model-evaluations:" + e.OwnerID).Error; err != nil { return err }
		}
		var previous modelEvaluationRecord
		err := tx.Where("owner_id = ? AND tenant_id = ? AND idempotency_key = ?", e.OwnerID, e.TenantID, e.IdempotencyKey).First(&previous).Error
		if err == nil {
			if previous.RequestSHA256 != e.RequestSHA256 { return me.ErrConflict }
			e, err = previous.evaluation()
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) { return err }
		var count int64
		if err := tx.Model(&modelEvaluationRecord{}).Where("owner_id = ? AND state IN ?", e.OwnerID, []string{me.Creating, me.Submitted, me.Running}).Count(&count).Error; err != nil { return err }
		if count >= maxActiveModelEvaluations { return me.ErrQuota }
		if err := validateEvaluationSources(tx, e); err != nil { return err }
		raw, err := json.Marshal(e)
		if err != nil { return err }
		specJSON, err := json.Marshal(e.JobSpec)
		if err != nil { return err }
		r := modelEvaluationRecord{
			ID: e.ID, ModelID: e.ModelID, VersionID: e.VersionID, DatasetID: e.Dataset.ID, DatasetVersionID: e.Dataset.VersionID,
			DatasetVisibility: e.Dataset.Visibility, DatasetTenantID: e.Dataset.TenantID, EvaluatorID: e.Evaluator.ID,
			OwnerID: e.OwnerID, TenantID: e.TenantID, JobID: e.JobID, SnapshotJSON: string(raw), State: e.State,
			ReportState: e.ReportState, ReportJSON: "null", JobSpecJSON: string(specJSON), Revision: 1, IdempotencyKey: e.IdempotencyKey,
			RequestSHA256: e.RequestSHA256, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
		if result.Error != nil { return result.Error }
		if result.RowsAffected != 1 { return me.ErrConflict }
		created = true
		return writeEvaluationAudit(tx, e.ID, "evaluation.reserved", e.OwnerID, e.CreatedAt)
	})
	return e, created && err == nil, err
}

func validateEvaluationSources(tx *gorm.DB, e me.Evaluation) error {
	var model ml.Model
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND archived = ?", e.ModelID, false).First(&model).Error; err != nil { return evaluationReadError(err) }
	var version ml.Version
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND model_id = ?", e.VersionID, e.ModelID).First(&version).Error; err != nil { return evaluationReadError(err) }
	if version.State != ml.Ready || version.SHA256 != e.ModelSHA256 || version.FileName != e.FileName { return me.ErrNotReady }
	var dataset DatasetRecord
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", e.Dataset.ID).First(&dataset).Error; err != nil { return evaluationReadError(err) }
	var dataVersion DatasetVersionRecord
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND dataset_id = ?", e.Dataset.VersionID, e.Dataset.ID).First(&dataVersion).Error; err != nil { return evaluationReadError(err) }
	if dataVersion.State != string(domain.DatasetVersionReady) || dataVersion.ManifestSHA256 == nil || *dataVersion.ManifestSHA256 != e.Dataset.ManifestSHA256 || dataVersion.SchemaVersion != e.Dataset.SchemaVersion { return me.ErrNotReady }
	samples := dataVersion.ValSamples
	if e.Dataset.Split == "test" { samples = dataVersion.TestSamples }
	if samples != e.Dataset.SampleCount { return me.ErrConflict }
	tenant := ""
	if dataset.OwnerTenantID != nil { tenant = *dataset.OwnerTenantID }
	if dataset.Visibility != e.Dataset.Visibility || tenant != e.Dataset.TenantID { return me.ErrConflict }
	var evaluator modelEvaluatorRecord
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", e.Evaluator.ID).First(&evaluator).Error; err != nil { return evaluationReadError(err) }
	current, err := evaluator.evaluationEvaluator()
	if err != nil { return err }
	if !current.Active { return me.ErrNotReady }
	current.CreatedAt = current.CreatedAt.UTC()
	frozenEvaluator := e.Evaluator
	frozenEvaluator.CreatedAt = frozenEvaluator.CreatedAt.UTC()
	// The executable snapshot is copied from this approved evaluator row, never
	// from caller-selected code, images, or commands.
	left, err := json.Marshal(current)
	if err != nil { return err }
	right, err := json.Marshal(frozenEvaluator)
	if err != nil { return err }
	if string(left) != string(right) { return me.ErrConflict }
	return nil
}

func validEvaluationDigest(value string) bool {
	if len(value) != 64 { return false }
	for _, ch := range value { if !strings.ContainsRune("0123456789abcdef", ch) { return false } }
	return true
}

func writeEvaluationAudit(tx *gorm.DB, id, action, actor string, now time.Time) error {
	return tx.Exec("INSERT INTO model_evaluation_audits (id, evaluation_id, actor_id, action, created_at) VALUES (?, ?, ?, ?, ?)", uuid.NewString(), id, actor, action, now).Error
}

func (s *ModelEvaluationRepository) MarkEvaluationSubmitted(ctx context.Context, id, jobID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job JobRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).First(&job).Error; err != nil { return evaluationReadError(err) }
		var r modelEvaluationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", id, jobID).First(&r).Error; err != nil { return evaluationReadError(err) }
		if err := evaluationJobMatches(r, job); err != nil { return err }
		if r.State != me.Creating { return nil }
		now := time.Now().UTC()
		if err := tx.Model(&r).Updates(map[string]any{"state": me.Submitted, "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil { return err }
		return writeEvaluationAudit(tx, r.ID, "evaluation.submitted", r.OwnerID, now)
	})
}

func (s *ModelEvaluationRepository) FailEvaluationSubmission(ctx context.Context, id, message string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEvaluationSubmission(tx, id); err != nil { return err }
		var r modelEvaluationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&r).Error; err != nil { return evaluationReadError(err) }
		if r.State != me.Creating { return nil }
		var count int64
		if err := tx.Model(&JobRecord{}).Where("id = ?", r.JobID).Count(&count).Error; err != nil { return err }
		if count != 0 { return me.ErrConflict }
		now := time.Now().UTC()
		// Submission errors can contain credentials or internal object paths.
		// Store only this stable, public diagnosis; detailed logs stay server-side.
		if err := tx.Model(&r).Updates(map[string]any{"state": me.Failed, "report_state": me.ReportMissing, "error": "evaluation job submission failed", "revision": gorm.Expr("revision + 1"), "updated_at": now, "finished_at": now}).Error; err != nil { return err }
		return writeEvaluationAudit(tx, r.ID, "evaluation.submission_failed", r.OwnerID, now)
	})
}

func evaluationJobMatches(r modelEvaluationRecord, job JobRecord) error {
	if r.JobID != job.ID || r.OwnerID != job.UserID || r.TenantID != job.TenantID || job.TrainingEngine != string(domain.TrainingEngineRayTrain) || job.SubmissionOrigin != string(domain.SubmissionOriginEvaluation) || job.ExternalSubmissionID != r.ID { return me.ErrConflict }
	return nil
}
