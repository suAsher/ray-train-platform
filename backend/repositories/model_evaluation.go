package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	me "ray-train-platform-backend/modelevaluation"
)

type ModelEvaluationRepository struct { db *gorm.DB }

func NewModelEvaluationRepository(db *gorm.DB) *ModelEvaluationRepository {
	return &ModelEvaluationRepository{db: db}
}

type modelEvaluatorRecord struct {
	ID string `gorm:"primaryKey"`
	Name string
	OwnerID string
	TenantID string
	SnapshotJSON string `gorm:"type:jsonb"`
	Active bool
	Revision int64
	CreatedAt time.Time
}

func (modelEvaluatorRecord) TableName() string { return "model_evaluators" }

type modelEvaluationRecord struct {
	ID string `gorm:"primaryKey"`
	ModelID string
	VersionID string
	DatasetID string
	DatasetVersionID string
	DatasetVisibility string
	DatasetTenantID string
	EvaluatorID string
	OwnerID string
	TenantID string
	JobID string
	SnapshotJSON string `gorm:"type:jsonb"`
	State string
	JobSpecJSON string `gorm:"type:jsonb"`
	ReportState string
	ReportJSON string `gorm:"type:jsonb"`
	ReportSHA256 string
	Error string
	Revision int64
	IdempotencyKey string
	RequestSHA256 string
	CreatedAt time.Time
	UpdatedAt time.Time
	FinishedAt *time.Time
}

func (modelEvaluationRecord) TableName() string { return "model_evaluations" }

func evaluationReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) { return me.ErrNotFound }
	if errors.Is(err, gorm.ErrDuplicatedKey) { return me.ErrConflict }
	return err
}

func (r modelEvaluatorRecord) evaluationEvaluator() (me.Evaluator, error) {
	var e me.Evaluator
	if err := json.Unmarshal([]byte(r.SnapshotJSON), &e); err != nil { return e, err }
	e.ID, e.Active, e.Revision, e.CreatedAt = r.ID, r.Active, r.Revision, r.CreatedAt
	return e, nil
}

func (r modelEvaluationRecord) evaluation() (me.Evaluation, error) {
	var e me.Evaluation
	if err := json.Unmarshal([]byte(r.SnapshotJSON), &e); err != nil { return e, err }
	// PostgreSQL JSONB normalizes object whitespace and key order. Recreate
	// the canonical config bytes before handing them to the runtime verifier.
	canonical, digest, err := me.CanonicalConfig(e.Config)
	if err != nil || digest != e.ConfigSHA256 { return me.Evaluation{}, me.ErrInvalid }
	e.Config = canonical
	e.ID, e.JobID, e.State, e.ReportState = r.ID, r.JobID, r.State, r.ReportState
	if err := json.Unmarshal([]byte(r.JobSpecJSON), &e.JobSpec); err != nil { return e, err }
	e.Error, e.ReportSHA256, e.Revision = r.Error, r.ReportSHA256, r.Revision
	e.CreatedAt, e.UpdatedAt, e.FinishedAt = r.CreatedAt, r.UpdatedAt, r.FinishedAt
	e.IdempotencyKey, e.RequestSHA256 = r.IdempotencyKey, r.RequestSHA256
	if r.ReportJSON != "" && r.ReportJSON != "null" {
		if err := json.Unmarshal([]byte(r.ReportJSON), &e.Report); err != nil { return e, err }
	}
	return e, nil
}

func (s *ModelEvaluationRepository) CreateEvaluator(ctx context.Context, e me.Evaluator) (me.Evaluator, error) {
	if e.ID == "" { e.ID = uuid.NewString() }
	e.Active, e.Revision, e.CreatedAt = true, 1, time.Now().UTC().Truncate(time.Microsecond)
	if err := me.ValidateEvaluator(e); err != nil { return me.Evaluator{}, err }
	if e.OwnerID == "" || e.TenantID == "" { return me.Evaluator{}, me.ErrInvalid }
	raw, err := json.Marshal(e)
	if err != nil { return me.Evaluator{}, err }
	r := modelEvaluatorRecord{ID: e.ID, Name: e.Name, OwnerID: e.OwnerID, TenantID: e.TenantID, SnapshotJSON: string(raw), Active: true, Revision: 1, CreatedAt: e.CreatedAt}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
	if result.Error != nil { return me.Evaluator{}, evaluationReadError(result.Error) }
	if result.RowsAffected != 1 { return me.Evaluator{}, me.ErrConflict }
	return e, nil
}

func (s *ModelEvaluationRepository) GetEvaluator(ctx context.Context, id string) (me.Evaluator, error) {
	var r modelEvaluatorRecord
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&r).Error; err != nil { return me.Evaluator{}, evaluationReadError(err) }
	return r.evaluationEvaluator()
}

func (s *ModelEvaluationRepository) ListEvaluators(ctx context.Context, includeInactive bool) ([]me.Evaluator, error) {
	query := s.db.WithContext(ctx)
	if !includeInactive { query = query.Where("active = ?", true) }
	var records []modelEvaluatorRecord
	if err := query.Order("created_at DESC, id DESC").Limit(200).Find(&records).Error; err != nil { return nil, err }
	items := make([]me.Evaluator, 0, len(records))
	for _, r := range records {
		e, err := r.evaluationEvaluator()
		if err != nil { return nil, err }
		items = append(items, e)
	}
	return items, nil
}

func (s *ModelEvaluationRepository) SetEvaluatorActive(ctx context.Context, id string, active bool, expectedRevision int64) (me.Evaluator, error) {
	if expectedRevision < 1 { return me.Evaluator{}, me.ErrInvalid }
	var e me.Evaluator
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&modelEvaluatorRecord{}).Where("id = ? AND revision = ?", id, expectedRevision).Updates(map[string]any{"active": active, "revision": gorm.Expr("revision + 1")})
		if result.Error != nil { return result.Error }
		if result.RowsAffected != 1 { return me.ErrConflict }
		var r modelEvaluatorRecord
		if err := tx.Where("id = ?", id).First(&r).Error; err != nil { return err }
		var err error
		e, err = r.evaluationEvaluator()
		return err
	})
	return e, err
}

func evaluationVisible(query *gorm.DB, tenant string, admin bool) *gorm.DB {
	if admin { return query }
	return query.Where("dataset_visibility = ? OR (dataset_visibility = ? AND dataset_tenant_id = ?)", me.Public, me.Team, tenant)
}

func (s *ModelEvaluationRepository) GetEvaluation(ctx context.Context, id, tenantID string, admin bool) (me.Evaluation, error) {
	var r modelEvaluationRecord
	if err := evaluationVisible(s.db.WithContext(ctx), tenantID, admin).Where("id = ?", id).First(&r).Error; err != nil { return me.Evaluation{}, evaluationReadError(err) }
	return r.evaluation()
}

func (s *ModelEvaluationRepository) GetEvaluationByJobID(ctx context.Context, jobID string) (me.Evaluation, error) {
	var r modelEvaluationRecord
	if err := s.db.WithContext(ctx).Where("job_id = ?", jobID).First(&r).Error; err != nil { return me.Evaluation{}, evaluationReadError(err) }
	return r.evaluation()
}

func (s *ModelEvaluationRepository) FindEvaluationRequest(ctx context.Context, tenantID, ownerID, key string) (me.Evaluation, error) {
	if tenantID == "" || ownerID == "" || key == "" { return me.Evaluation{}, me.ErrInvalid }
	var r modelEvaluationRecord
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND owner_id = ? AND idempotency_key = ?", tenantID, ownerID, key).First(&r).Error; err != nil { return me.Evaluation{}, evaluationReadError(err) }
	return r.evaluation()
}

func (s *ModelEvaluationRepository) ListEvaluations(ctx context.Context, f me.Filter) (me.EvaluationPage, error) {
	page := me.EvaluationPage{Items: []me.Evaluation{}}
	query := evaluationVisible(s.db.WithContext(ctx), f.TenantID, f.SuperAdmin)
	for column, value := range map[string]string{"owner_id": f.OwnerID, "model_id": f.ModelID, "version_id": f.VersionID, "state": f.State} {
		if value != "" { query = query.Where(column + " = ?", value) }
	}
	if f.Cursor != "" { query = query.Where("id > ?", f.Cursor) }
	limit := modelLimit(f.Limit)
	var records []modelEvaluationRecord
	if err := query.Order("id ASC").Limit(limit + 1).Find(&records).Error; err != nil { return page, err }
	if len(records) > limit { records = records[:limit]; page.NextCursor = records[limit-1].ID }
	for _, r := range records {
		e, err := r.evaluation()
		if err != nil { return page, err }
		page.Items = append(page.Items, e)
	}
	return page, nil
}
