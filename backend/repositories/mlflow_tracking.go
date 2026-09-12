package repositories

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	tracking "ray-train-platform-backend/mlflowtracking"
)

type MLflowTrackingExperimentRecord struct {
	ID              string `gorm:"primaryKey"`
	TenantID        string `gorm:"uniqueIndex:mlflow_tracking_exp_key;uniqueIndex:mlflow_tracking_exp_name"`
	UserID          string `gorm:"uniqueIndex:mlflow_tracking_exp_key;uniqueIndex:mlflow_tracking_exp_name"`
	IdempotencyHash string `gorm:"uniqueIndex:mlflow_tracking_exp_key"`
	Name            string `gorm:"uniqueIndex:mlflow_tracking_exp_name"`
	State           string
	UpstreamID      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (MLflowTrackingExperimentRecord) TableName() string { return "mlflow_tracking_experiments" }

type MLflowTrackingRunRecord struct {
	ID              string `gorm:"primaryKey"`
	ExperimentID    string
	TenantID        string `gorm:"uniqueIndex:mlflow_tracking_run_key"`
	UserID          string `gorm:"uniqueIndex:mlflow_tracking_run_key"`
	IdempotencyHash string `gorm:"uniqueIndex:mlflow_tracking_run_key"`
	Name            string
	State           string
	UpstreamID      string
	StartTimeMS     int64
	EndTimeMS       int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
	FinishStatus    string
	LeaseID         string
	LeaseExpiresAt  *time.Time
}

func (MLflowTrackingRunRecord) TableName() string { return "mlflow_tracking_runs" }

type MLflowTrackingStore struct{ db *gorm.DB }

func NewMLflowTrackingStore(db *gorm.DB) *MLflowTrackingStore { return &MLflowTrackingStore{db: db} }

func trackingExpRecord(value tracking.Experiment) MLflowTrackingExperimentRecord {
	return MLflowTrackingExperimentRecord{value.ID, value.TenantID, value.UserID, value.IdempotencyHash, value.Name, value.State, value.UpstreamID, value.CreatedAt, value.UpdatedAt}
}
func trackingExpValue(value MLflowTrackingExperimentRecord) tracking.Experiment {
	return tracking.Experiment{ID: value.ID, TenantID: value.TenantID, UserID: value.UserID, IdempotencyHash: value.IdempotencyHash, Name: value.Name, State: value.State, UpstreamID: value.UpstreamID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}
func trackingRunRecord(v tracking.Run) MLflowTrackingRunRecord {
	return MLflowTrackingRunRecord{v.ID, v.ExperimentID, v.TenantID, v.UserID, v.IdempotencyHash, v.Name, v.State, v.UpstreamID, v.StartTimeMS, v.EndTimeMS, v.CreatedAt, v.UpdatedAt, v.FinishedAt, v.FinishStatus, v.LeaseID, v.LeaseExpiresAt}
}
func trackingRunValue(v MLflowTrackingRunRecord) tracking.Run {
	return tracking.Run{ID: v.ID, ExperimentID: v.ExperimentID, TenantID: v.TenantID, UserID: v.UserID, IdempotencyHash: v.IdempotencyHash, Name: v.Name, State: v.State, UpstreamID: v.UpstreamID, StartTimeMS: v.StartTimeMS, EndTimeMS: v.EndTimeMS, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, FinishedAt: v.FinishedAt, FinishStatus: v.FinishStatus, LeaseID: v.LeaseID, LeaseExpiresAt: v.LeaseExpiresAt}
}

func scopedTracking(db *gorm.DB, actor tracking.Actor) *gorm.DB {
	return db.Where("tenant_id = ? AND user_id = ?", actor.TenantID, actor.UserID)
}
func trackingReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tracking.ErrNotFound
	}
	return err
}

func (s *MLflowTrackingStore) ReserveExperiment(ctx context.Context, input tracking.Experiment) (tracking.Experiment, bool, error) {
	record := trackingExpRecord(input)
	insert := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if insert.Error != nil {
		return tracking.Experiment{}, false, insert.Error
	}
	if insert.RowsAffected == 1 {
		return trackingExpValue(record), true, nil
	}
	var existing MLflowTrackingExperimentRecord
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND idempotency_hash = ?", input.TenantID, input.UserID, input.IdempotencyHash).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tracking.Experiment{}, false, tracking.ErrConflict
	}
	if err != nil {
		return tracking.Experiment{}, false, err
	}
	if existing.Name != input.Name {
		return tracking.Experiment{}, false, tracking.ErrConflict
	}
	return trackingExpValue(existing), false, nil
}

func (s *MLflowTrackingStore) GetExperiment(ctx context.Context, actor tracking.Actor, id string) (tracking.Experiment, error) {
	var record MLflowTrackingExperimentRecord
	err := scopedTracking(s.db.WithContext(ctx), actor).Where("id = ?", id).First(&record).Error
	return trackingExpValue(record), trackingReadError(err)
}

func (s *MLflowTrackingStore) CompleteExperiment(ctx context.Context, actor tracking.Actor, id, upstream string) (tracking.Experiment, error) {
	if upstream == "" {
		return tracking.Experiment{}, tracking.ErrInvalid
	}
	result := scopedTracking(s.db.WithContext(ctx).Model(&MLflowTrackingExperimentRecord{}), actor).Where("id = ? AND state = 'PENDING'", id).Updates(map[string]any{"state": "READY", "upstream_id": upstream, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return tracking.Experiment{}, result.Error
	}
	record, err := s.GetExperiment(ctx, actor, id)
	if err != nil {
		return record, err
	}
	if record.UpstreamID != upstream || record.State != "READY" {
		return record, tracking.ErrConflict
	}
	return record, nil
}

func (s *MLflowTrackingStore) ListExperiments(ctx context.Context, actor tracking.Actor, after string, limit int) ([]tracking.Experiment, error) {
	if limit < 1 || limit > 101 {
		return nil, tracking.ErrInvalid
	}
	var rows []MLflowTrackingExperimentRecord
	query := scopedTracking(s.db.WithContext(ctx), actor).Where("id > ?", after).Order("id ASC").Limit(limit)
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]tracking.Experiment, 0, len(rows))
	for _, row := range rows {
		items = append(items, trackingExpValue(row))
	}
	return items, nil
}

func (s *MLflowTrackingStore) ReserveRun(ctx context.Context, input tracking.Run) (tracking.Run, bool, error) {
	actor := tracking.Actor{TenantID: input.TenantID, UserID: input.UserID}
	experiment, err := s.GetExperiment(ctx, actor, input.ExperimentID)
	if err != nil {
		return tracking.Run{}, false, err
	}
	if experiment.State != "READY" {
		return tracking.Run{}, false, tracking.ErrPending
	}
	record := trackingRunRecord(input)
	insert := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if insert.Error != nil {
		return tracking.Run{}, false, insert.Error
	}
	if insert.RowsAffected == 1 {
		return trackingRunValue(record), true, nil
	}
	var existing MLflowTrackingRunRecord
	err = s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND idempotency_hash = ?", input.TenantID, input.UserID, input.IdempotencyHash).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tracking.Run{}, false, tracking.ErrConflict
	}
	if err != nil {
		return tracking.Run{}, false, err
	}
	if existing.Name != input.Name || existing.ExperimentID != input.ExperimentID {
		return tracking.Run{}, false, tracking.ErrConflict
	}
	return trackingRunValue(existing), false, nil
}

func (s *MLflowTrackingStore) GetRun(ctx context.Context, actor tracking.Actor, id string) (tracking.Run, error) {
	var record MLflowTrackingRunRecord
	err := scopedTracking(s.db.WithContext(ctx), actor).Where("id = ?", id).First(&record).Error
	return trackingRunValue(record), trackingReadError(err)
}

func (s *MLflowTrackingStore) CompleteRun(ctx context.Context, actor tracking.Actor, id, upstream string) (tracking.Run, error) {
	if upstream == "" {
		return tracking.Run{}, tracking.ErrInvalid
	}
	result := scopedTracking(s.db.WithContext(ctx).Model(&MLflowTrackingRunRecord{}), actor).Where("id = ? AND state = 'PENDING'", id).Updates(map[string]any{"state": "RUNNING", "upstream_id": upstream, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return tracking.Run{}, result.Error
	}
	record, err := s.GetRun(ctx, actor, id)
	if err != nil {
		return record, err
	}
	if record.UpstreamID != upstream || record.State == "PENDING" {
		return record, tracking.ErrConflict
	}
	return record, nil
}

func (s *MLflowTrackingStore) ListRuns(ctx context.Context, actor tracking.Actor, experimentID, after string, limit int) ([]tracking.Run, error) {
	if limit < 1 || limit > 101 {
		return nil, tracking.ErrInvalid
	}
	var rows []MLflowTrackingRunRecord
	query := scopedTracking(s.db.WithContext(ctx), actor).Where("experiment_id = ? AND id > ?", experimentID, after).Order("id ASC").Limit(limit)
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]tracking.Run, 0, len(rows))
	for _, row := range rows {
		items = append(items, trackingRunValue(row))
	}
	return items, nil
}

var _ tracking.Store = (*MLflowTrackingStore)(nil)
