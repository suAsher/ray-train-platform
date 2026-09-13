package repositories

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelregistry"
	"time"
)

type ModelRegistryStore struct{ db *gorm.DB }

func NewModelRegistryStore(db *gorm.DB) *ModelRegistryStore { return &ModelRegistryStore{db: db} }

var _ mr.Store = (*ModelRegistryStore)(nil)

// A dashboard deep link may target the dedicated Registry copy Run rather than
// a training Run. Resolve it only from one completed, persisted model snapshot.
// Archived model versions remain readable in the shared catalog.
func (s *ModelRegistryStore) GetReadyByRunID(ctx context.Context, runID string) (mr.Record, error) {
	var records []mr.Record
	err := s.db.WithContext(ctx).Model(&mr.Record{}).Select("model_registry_links.*").
		Joins("JOIN model_versions ON model_versions.id = model_registry_links.version_id AND model_versions.state = ?", ml.Ready).
		Joins("JOIN model_catalog ON model_catalog.id = model_versions.model_id").
		Where("model_registry_links.run_id = ? AND model_registry_links.state = ?", runID, "READY").Limit(2).Find(&records).Error
	if err != nil {
		return mr.Record{}, err
	}
	if len(records) == 0 {
		return mr.Record{State: "NOT_LINKED"}, nil
	}
	if len(records) != 1 {
		return mr.Record{}, mr.ErrConflict
	}
	return records[0], nil
}

func (s *ModelRegistryStore) Get(ctx context.Context, id string) (mr.Record, error) {
	var r mr.Record
	err := s.db.WithContext(ctx).Where("version_id = ?", id).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return mr.Record{VersionID: id, State: "NOT_LINKED"}, nil
	}
	if err == nil && r.State == "SYNCING" && r.LeaseExpiresAt != nil && !r.LeaseExpiresAt.After(time.Now().UTC()) {
		r.State = "FAILED"
		r.Error = "上次关联操作已超时，请重新同步；已有注册记录会先核对后复用"
	}
	return r, err
}
func (s *ModelRegistryStore) Acquire(ctx context.Context, modelID, versionID, actorID string, super bool) (mr.Record, bool, error) {
	var record mr.Record
	acquired := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m ml.Model
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", modelID).First(&m).Error; err != nil {
			return modelReadError(err)
		}
		if m.Archived || (!super && m.OwnerID != actorID) {
			return ml.ErrConflict
		}
		var v ml.Version
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND model_id = ?", versionID, modelID).First(&v).Error; err != nil {
			return modelReadError(err)
		}
		if v.State != ml.Ready {
			return ml.ErrNotReady
		}
		now := time.Now().UTC()
		fresh := mr.Record{VersionID: versionID, State: "PENDING", Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&fresh).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("version_id = ?", versionID).First(&record).Error; err != nil {
			return err
		}
		if record.State == "READY" || (record.LeaseExpiresAt != nil && record.LeaseExpiresAt.After(now)) {
			return nil
		}
		lease := uuid.NewString()
		expiry := now.Add(mr.LeaseDuration)
		changes := map[string]any{"state": "SYNCING", "error": "", "lease_id": lease, "lease_expires_at": expiry, "revision": gorm.Expr("revision + 1"), "updated_at": now}
		if err := tx.Model(&mr.Record{}).Where("version_id = ?", versionID).Updates(changes).Error; err != nil {
			return err
		}
		record.State = "SYNCING"
		record.Error = ""
		record.LeaseID = lease
		record.LeaseExpiresAt = &expiry
		record.Revision++
		record.UpdatedAt = now
		acquired = true
		return nil
	})
	return record, acquired, err
}
func (s *ModelRegistryStore) Renew(ctx context.Context, id, lease string) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&mr.Record{}).Where("version_id = ? AND state = 'SYNCING' AND lease_id = ? AND lease_expires_at > ?", id, lease, now).Updates(map[string]any{"lease_expires_at": now.Add(mr.LeaseDuration), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return mr.ErrConflict
	}
	return nil
}
func (s *ModelRegistryStore) Complete(ctx context.Context, id, lease string, link mr.Link, failure string) error {
	now := time.Now().UTC()
	changes := map[string]any{"state": "READY", "error": "", "registered_name": link.RegisteredName, "registry_version": link.Version, "run_id": link.RunID, "source_uri": link.SourceURI, "lease_id": "", "lease_expires_at": nil, "revision": gorm.Expr("revision + 1"), "updated_at": now}
	if failure != "" {
		changes["state"] = "FAILED"
		changes["error"] = failure
	}
	result := s.db.WithContext(ctx).Model(&mr.Record{}).Where("version_id = ? AND state = 'SYNCING' AND lease_id = ? AND lease_expires_at > ?", id, lease, now).Updates(changes)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return mr.ErrConflict
	}
	return nil
}
