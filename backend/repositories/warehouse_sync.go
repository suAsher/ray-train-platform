package repositories

import (
	"context"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	ws "ray-train-platform-backend/warehousesync"
)

type WarehouseSyncStore struct{ db *gorm.DB }

var _ ws.Store = (*WarehouseSyncStore)(nil)

// SQLite is used only by unit tests. PostgreSQL serializes owner reservations
// across replicas with transaction advisory locks instead of a process mutex.
var warehouseSyncSQLiteMu sync.Mutex

func NewWarehouseSyncStore(db *gorm.DB) *WarehouseSyncStore { return &WarehouseSyncStore{db: db} }

func (s *WarehouseSyncStore) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	if s.db.Dialector.Name() == "sqlite" {
		warehouseSyncSQLiteMu.Lock()
		defer warehouseSyncSQLiteMu.Unlock()
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

func lockWarehouseSyncOwner(tx *gorm.DB, owner string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "warehouse-sync-owner:"+owner).Error
}

func warehouseSyncActive() []string {
	return []string{ws.WaitingSource, ws.Queued, ws.Uploading, ws.Registering, ws.WaitingReauth}
}
func warehouseSyncQuota(tx *gorm.DB, owner string) error {
	var count int64
	if err := tx.Model(&ws.Operation{}).Where("owner_id = ? AND state IN ?", owner, warehouseSyncActive()).Count(&count).Error; err != nil {
		return err
	}
	if count >= 16 {
		return ws.ErrQuota
	}
	return nil
}

func (s *WarehouseSyncStore) Create(ctx context.Context, op ws.Operation) (ws.Operation, error) {
	if op.ID == "" || op.OwnerID == "" || op.TenantID == "" || op.IdempotencyKey == "" || op.RequestSHA256 == "" || len(op.Credential) == 0 || (op.State != ws.Queued && op.State != ws.WaitingSource) {
		return ws.Operation{}, ws.ErrInvalid
	}
	var result ws.Operation
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockWarehouseSyncOwner(tx, op.OwnerID); err != nil {
			return err
		}
		var prior ws.Operation
		err := tx.Where("owner_id = ? AND tenant_id = ? AND idempotency_key = ?", op.OwnerID, op.TenantID, op.IdempotencyKey).First(&prior).Error
		if err == nil {
			if prior.RequestSHA256 != op.RequestSHA256 {
				return ws.ErrConflict
			}
			result = prior
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := warehouseSyncQuota(tx, op.OwnerID); err != nil {
			return err
		}
		now := time.Now().UTC()
		op.CreatedAt = now
		op.UpdatedAt = now
		op.LeaseID = ""
		op.LeaseExpiresAt = nil
		if op.NextAttemptAt.IsZero() {
			op.NextAttemptAt = now
		}
		if op.Paths == nil {
			op.Paths = []string{}
		}
		if op.Files == nil {
			op.Files = []ws.File{}
		}
		if err := tx.Create(&op).Error; err != nil {
			return err
		}
		result = op
		return nil
	})
	return result, err
}

func warehouseSyncReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ws.ErrNotFound
	}
	return err
}
func (s *WarehouseSyncStore) Get(ctx context.Context, id string) (ws.Operation, error) {
	var op ws.Operation
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&op).Error
	return op, warehouseSyncReadError(err)
}
func (s *WarehouseSyncStore) ListJob(ctx context.Context, owner, tenant, job string) ([]ws.Operation, error) {
	result := []ws.Operation{}
	err := s.db.WithContext(ctx).Where("owner_id = ? AND tenant_id = ? AND job_id = ?", owner, tenant, job).Order("created_at DESC, id DESC").Limit(100).Find(&result).Error
	return result, err
}

func (s *WarehouseSyncStore) Claim(ctx context.Context, lease string, now, expires time.Time) (ws.Operation, error) {
	if lease == "" || !expires.After(now) {
		return ws.Operation{}, ws.ErrInvalid
	}
	var result ws.Operation
	found := false
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		// Never reclaim a lost writer. A create request may already have reached the
		// remote warehouse, so REGISTERING must remain explicitly uncertain.
		for _, item := range []struct {
			registering    bool
			state, message string
		}{
			{true, ws.Unknown, "同步中断，功能仓创建结果待确认；请核对后再发起新同步。"},
			{false, ws.Failed, "同步工作进程已中断，请重新授权后重试。"},
		} {
			query := tx.Model(&ws.Operation{}).Where("lease_id <> '' AND lease_expires_at <= ?", now)
			if item.registering {
				query = query.Where("state = ?", ws.Registering)
			} else {
				query = query.Where("state <> ?", ws.Registering)
			}
			if err := query.Updates(map[string]any{"state": item.state, "message": item.message, "credential": nil, "lease_id": "", "lease_expires_at": nil, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state IN ? AND lease_id = '' AND next_attempt_at <= ?", []string{ws.WaitingSource, ws.Queued}, now).Order("next_attempt_at, created_at, id").First(&result).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		update := tx.Model(&ws.Operation{}).Where("id = ? AND lease_id = ''", result.ID).Updates(map[string]any{"lease_id": lease, "lease_expires_at": expires, "updated_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ws.ErrConflict
		}
		result.LeaseID = lease
		result.LeaseExpiresAt = &expires
		result.UpdatedAt = now
		found = true
		return nil
	})
	if err == nil && !found {
		err = ws.ErrNotFound
	}
	return result, err
}

func (s *WarehouseSyncStore) Save(ctx context.Context, op ws.Operation, lease string) error {
	if op.ID == "" || lease == "" || (op.LeaseID != "" && op.LeaseID != lease) {
		return ws.ErrConflict
	}
	op.UpdatedAt = time.Now().UTC()
	fields := []string{"state", "message", "files", "run_id", "experiment_id", "target_version_id", "next_attempt_at", "credential", "lease_id", "updated_at"}
	// A worker snapshot must not undo a concurrent heartbeat extension.
	if op.LeaseID == "" {
		op.LeaseExpiresAt = nil
		fields = append(fields, "lease_expires_at")
	}
	// Select prevents worker snapshots from changing frozen source or target
	// identity and still writes zero values, released leases and erased secrets.
	result := s.db.WithContext(ctx).Model(&ws.Operation{}).Where("id = ? AND lease_id = ? AND lease_expires_at > ?", op.ID, lease, op.UpdatedAt).
		Select(fields).Updates(&op)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ws.ErrConflict
	}
	return nil
}

func (s *WarehouseSyncStore) Renew(ctx context.Context, id, lease string, expires time.Time) error {
	now := time.Now().UTC()
	if lease == "" || !expires.After(now) {
		return ws.ErrConflict
	}
	result := s.db.WithContext(ctx).Model(&ws.Operation{}).Where("id = ? AND lease_id = ? AND lease_expires_at > ?", id, lease, now).Updates(map[string]any{"lease_expires_at": expires, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ws.ErrConflict
	}
	return nil
}

func loadWarehouseSyncActor(tx *gorm.DB, id string, actor ws.Actor) (ws.Operation, error) {
	var op ws.Operation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_id = ? AND tenant_id = ?", id, actor.ID, actor.TenantID).First(&op).Error
	return op, warehouseSyncReadError(err)
}

func (s *WarehouseSyncStore) Resume(ctx context.Context, id string, actor ws.Actor, credential []byte) (ws.Operation, error) {
	if len(credential) == 0 {
		return ws.Operation{}, ws.ErrInvalid
	}
	var op ws.Operation
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockWarehouseSyncOwner(tx, actor.ID); err != nil {
			return err
		}
		var err error
		op, err = loadWarehouseSyncActor(tx, id, actor)
		if err != nil {
			return err
		}
		if (op.State != ws.Failed && op.State != ws.WaitingReauth) || op.TargetVersionID != "" || op.LeaseID != "" || op.LeaseExpiresAt != nil {
			return ws.ErrConflict
		}
		if op.State == ws.Failed {
			if err := warehouseSyncQuota(tx, actor.ID); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		update := tx.Model(&ws.Operation{}).Where("id = ? AND owner_id = ? AND tenant_id = ? AND state IN ? AND target_version_id = '' AND lease_id = '' AND lease_expires_at IS NULL", id, actor.ID, actor.TenantID, []string{ws.Failed, ws.WaitingReauth}).Updates(map[string]any{"state": ws.Queued, "message": "", "credential": credential, "lease_id": "", "lease_expires_at": nil, "next_attempt_at": now, "updated_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ws.ErrConflict
		}
		return tx.Where("id = ?", id).First(&op).Error
	})
	return op, err
}

func (s *WarehouseSyncStore) Cancel(ctx context.Context, id string, actor ws.Actor) (ws.Operation, error) {
	var op ws.Operation
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var err error
		op, err = loadWarehouseSyncActor(tx, id, actor)
		if err != nil {
			return err
		}
		if op.TargetVersionID != "" {
			return ws.ErrConflict
		}
		switch op.State {
		case ws.WaitingSource, ws.Queued, ws.Uploading, ws.WaitingReauth:
		default:
			return ws.ErrConflict
		}
		update := tx.Model(&ws.Operation{}).Where("id = ? AND owner_id = ? AND tenant_id = ? AND state IN ? AND target_version_id = ''", id, actor.ID, actor.TenantID, []string{ws.WaitingSource, ws.Queued, ws.Uploading, ws.WaitingReauth}).Updates(map[string]any{"state": ws.Canceled, "message": "用户已取消同步。", "credential": nil, "lease_id": "", "lease_expires_at": nil, "updated_at": time.Now().UTC()})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ws.ErrConflict
		}
		return tx.Where("id = ?", id).First(&op).Error
	})
	return op, err
}
