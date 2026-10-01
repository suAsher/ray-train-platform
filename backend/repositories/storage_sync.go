package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	ss "ray-train-platform-backend/storagesync"
)

type StorageSyncRepository struct{ db *gorm.DB }

var _ ss.Repository = (*StorageSyncRepository)(nil)
var _ ss.Tx = (*storageSyncTx)(nil)

func NewStorageSyncRepository(database *gorm.DB) *StorageSyncRepository {
	return &StorageSyncRepository{db: database}
}

type storageSyncTx struct {
	db           *gorm.DB
	pendingLocks []storageSyncPathLockRecord
}

// A short global coordination lock makes the initial implementation safe across
// API replicas: admission, source read locks, and target write locks commit as a
// single decision. Data-plane IO never runs inside this transaction.
func (r *StorageSyncRepository) Transact(ctx context.Context, callback func(ss.Tx) error) error {
	if r == nil || r.db == nil || callback == nil {
		return ss.ErrInvalid
	}
	return storageSyncError(r.db.WithContext(ctx).Transaction(func(database *gorm.DB) error {
		if database.Dialector.Name() == "postgres" {
			if err := database.Exec("SELECT pg_advisory_xact_lock(?)", int64(728119058)).Error; err != nil {
				return err
			}
		}
		tx := &storageSyncTx{db: database}
		if err := callback(tx); err != nil {
			return err
		}
		return tx.storageSyncFlushLocks()
	}))
}

func (r *StorageSyncRepository) ListPlans(ctx context.Context) ([]ss.Plan, error) {
	return (&storageSyncTx{db: r.db.WithContext(ctx)}).ListPlans()
}

func (r *StorageSyncRepository) GetPlan(ctx context.Context, id string) (ss.Plan, error) {
	return storageSyncReadPlan(r.db.WithContext(ctx), id)
}

func (r *StorageSyncRepository) ListRuns(ctx context.Context, planID string) ([]ss.Run, error) {
	return (&storageSyncTx{db: r.db.WithContext(ctx)}).ListRuns(planID)
}

func (r *StorageSyncRepository) GetRun(ctx context.Context, id string) (ss.Run, error) {
	return storageSyncReadRun(r.db.WithContext(ctx), id)
}

func (r *StorageSyncRepository) GetPreview(ctx context.Context, id string) (ss.Preview, error) {
	return storageSyncReadPreview(r.db.WithContext(ctx), id)
}

func storageSyncReadPlan(database *gorm.DB, id string) (ss.Plan, error) {
	var row storageSyncPlanRecord
	if err := database.Where("id = ?", id).First(&row).Error; err != nil {
		return ss.Plan{}, storageSyncError(err)
	}
	var plan ss.Plan
	err := json.Unmarshal([]byte(row.SnapshotJSON), &plan)
	return plan, err
}

func (tx *storageSyncTx) GetPlan(id string) (ss.Plan, error) {
	return storageSyncReadPlan(tx.db.Clauses(clause.Locking{Strength: "UPDATE"}), id)
}

func (tx *storageSyncTx) ListPlans() ([]ss.Plan, error) {
	var rows []storageSyncPlanRecord
	if err := tx.db.Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	plans := make([]ss.Plan, 0, len(rows))
	for _, row := range rows {
		var plan ss.Plan
		if err := json.Unmarshal([]byte(row.SnapshotJSON), &plan); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (tx *storageSyncTx) PutPlan(plan ss.Plan) error {
	if plan.ID == "" || plan.Revision < 1 {
		return ss.ErrInvalid
	}
	old, err := tx.GetPlan(plan.ID)
	if err != nil && !errors.Is(err, ss.ErrNotFound) {
		return err
	}
	create := errors.Is(err, ss.ErrNotFound)
	if create && plan.Revision != 1 {
		return ss.ErrConflict
	}
	if !create && (plan.Revision < old.Revision || plan.Revision > old.Revision+1 ||
		!plan.CreatedAt.Equal(old.CreatedAt) || plan.CreatedBy != old.CreatedBy ||
		(plan.Revision == old.Revision && (plan.Name != old.Name || !reflect.DeepEqual(plan.Config, old.Config)))) {
		return ss.ErrConflict
	}
	snapshot, err := storageSyncJSON(plan)
	if err != nil {
		return err
	}
	row := storageSyncPlanRecord{ID: plan.ID, Revision: plan.Revision, SnapshotJSON: snapshot, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt}
	if create {
		err = tx.db.Create(&row).Error
	} else {
		err = tx.db.Model(&storageSyncPlanRecord{}).Where("id = ? AND revision = ?", plan.ID, old.Revision).Select("revision", "snapshot_json", "updated_at").Updates(&row).Error
	}
	if err != nil {
		return storageSyncError(err)
	}
	if create || plan.Revision != old.Revision {
		revision := storageSyncPlanRevisionRecord{PlanID: plan.ID, Revision: plan.Revision, SnapshotJSON: snapshot, CreatedAt: plan.UpdatedAt}
		return storageSyncError(tx.db.Create(&revision).Error)
	}
	return nil
}

func storageSyncReadPreview(database *gorm.DB, id string) (ss.Preview, error) {
	var row storageSyncPreviewRecord
	if err := database.Where("id = ?", id).First(&row).Error; err != nil {
		return ss.Preview{}, storageSyncError(err)
	}
	return storageSyncDecodePreview(row.SnapshotJSON)
}

func (tx *storageSyncTx) GetPreview(id string) (ss.Preview, error) {
	return storageSyncReadPreview(tx.db.Clauses(clause.Locking{Strength: "UPDATE"}), id)
}

func (tx *storageSyncTx) ListPreviews() ([]ss.Preview, error) {
	var rows []storageSyncPreviewRecord
	if err := tx.db.Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	previews := make([]ss.Preview, 0, len(rows))
	for _, row := range rows {
		preview, err := storageSyncDecodePreview(row.SnapshotJSON)
		if err != nil {
			return nil, err
		}
		previews = append(previews, preview)
	}
	return previews, nil
}

func (tx *storageSyncTx) PutPreview(preview ss.Preview) error {
	if preview.ID == "" {
		return ss.ErrInvalid
	}
	old, err := tx.GetPreview(preview.ID)
	if err != nil && !errors.Is(err, ss.ErrNotFound) {
		return err
	}
	create := errors.Is(err, ss.ErrNotFound)
	if !create && (preview.PlanID != old.PlanID || preview.Actor != old.Actor || preview.ConfigRevision != old.ConfigRevision ||
		preview.Attempt < old.Attempt || preview.Generation < old.Generation ||
		(preview.Attempt == old.Attempt && preview.Generation == old.Generation && preview.Sequence < old.Sequence) ||
		(preview.Attempt == old.Attempt && old.JobUID != "" && preview.JobUID != old.JobUID) ||
		(preview.Attempt == old.Attempt && old.WorkerID != "" && preview.WorkerID != old.WorkerID) ||
		!reflect.DeepEqual(preview.Config, old.Config) || !reflect.DeepEqual(preview.Resolved, old.Resolved)) {
		return ss.ErrConflict
	}
	snapshot, err := storageSyncEncodePreview(preview)
	if err != nil {
		return err
	}
	row := storageSyncPreviewRecord{ID: preview.ID, PlanID: preview.PlanID, Actor: preview.Actor, State: preview.State,
		Attempt: preview.Attempt, Generation: preview.Generation, Sequence: preview.Sequence, SnapshotJSON: snapshot,
		CreatedAt: preview.CreatedAt, ExpiresAt: preview.ExpiresAt}
	if create {
		return storageSyncError(tx.db.Create(&row).Error)
	}
	return storageSyncError(tx.db.Model(&storageSyncPreviewRecord{}).Where("id = ?", preview.ID).Select("*").Updates(&row).Error)
}

func storageSyncReadRun(database *gorm.DB, id string) (ss.Run, error) {
	var row storageSyncRunRecord
	if err := database.Where("id = ?", id).First(&row).Error; err != nil {
		return ss.Run{}, storageSyncError(err)
	}
	return storageSyncDecodeRun(row.SnapshotJSON)
}

func (tx *storageSyncTx) GetRun(id string) (ss.Run, error) {
	return storageSyncReadRun(tx.db.Clauses(clause.Locking{Strength: "UPDATE"}), id)
}

func (tx *storageSyncTx) ListRuns(planID string) ([]ss.Run, error) {
	query := tx.db.Order("created_at DESC, id")
	if planID != "" {
		query = query.Where("plan_id = ?", planID)
	}
	var rows []storageSyncRunRecord
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	runs := make([]ss.Run, 0, len(rows))
	for _, row := range rows {
		run, err := storageSyncDecodeRun(row.SnapshotJSON)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func storageSyncRunIdentity(run ss.Run) (string, error) {
	return storageSyncJSON(struct {
		Config           ss.Config
		Resolved         []ss.ResolvedMapping
		ResolutionDigest string
		PreviewID        string
		Trigger          string
	}{run.Config, run.Resolved, run.ResolutionDigest, run.PreviewID, run.Trigger})
}

func (tx *storageSyncTx) PutRun(run ss.Run) error {
	if run.ID == "" || run.Attempt < 1 || run.Generation < 1 || run.Sequence < 0 {
		return ss.ErrInvalid
	}
	old, err := tx.GetRun(run.ID)
	if err != nil && !errors.Is(err, ss.ErrNotFound) {
		return err
	}
	create := errors.Is(err, ss.ErrNotFound)
	if !create && (run.Attempt < old.Attempt || run.Generation < old.Generation ||
		(run.Attempt == old.Attempt && run.Generation == old.Generation && run.Sequence < old.Sequence) ||
		(run.Attempt == old.Attempt && old.WorkerID != "" && run.WorkerID != old.WorkerID) ||
		(run.Attempt > old.Attempt && run.Generation <= old.Generation)) {
		return ss.ErrConflict
	}
	snapshot, err := storageSyncEncodeRun(run)
	if err != nil {
		return err
	}
	identity, err := storageSyncRunIdentity(run)
	if err != nil {
		return err
	}
	row := storageSyncRunRecord{ID: run.ID, PlanID: run.PlanID, ConfigRevision: run.ConfigRevision,
		RequestedBy: run.RequestedBy, IdempotencyKey: run.IdempotencyKey, State: run.State, Attempt: run.Attempt,
		Generation: run.Generation, Sequence: run.Sequence, JobUID: run.JobUID, IdentityJSON: identity, SnapshotJSON: snapshot,
		ScheduledAt: run.ScheduledAt, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, FinishedAt: run.FinishedAt}
	if create {
		err = tx.db.Create(&row).Error
	} else {
		err = tx.db.Model(&storageSyncRunRecord{}).Where("id = ? AND attempt = ? AND generation = ? AND sequence = ?", run.ID, old.Attempt, old.Generation, old.Sequence).Select("*").Updates(&row).Error
	}
	if err != nil {
		return storageSyncError(err)
	}
	attempt := storageSyncAttemptRecord{RunID: run.ID, Attempt: run.Attempt, Generation: run.Generation, SnapshotJSON: snapshot, UpdatedAt: run.UpdatedAt}
	return storageSyncError(tx.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}, {Name: "attempt"}},
		DoUpdates: clause.AssignmentColumns([]string{"generation", "snapshot_json", "updated_at"})}).Create(&attempt).Error)
}
