package repositories

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	ml "ray-train-platform-backend/modellifecycle"
)

type ModelLifecycleStore struct{ db *gorm.DB }

func NewModelLifecycleStore(db *gorm.DB) *ModelLifecycleStore { return &ModelLifecycleStore{db: db} }

var _ ml.Repository = (*ModelLifecycleStore)(nil)

type modelAudit struct {
	ID        string `gorm:"primaryKey"`
	ModelID   string
	VersionID string
	ActorID   string
	ActorName string
	Action    string
	Details   string `gorm:"type:jsonb"`
	CreatedAt time.Time
}

func (modelAudit) TableName() string { return "model_audits" }
func modelReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ml.ErrNotFound
	}
	return err
}
func writeModelAudit(tx *gorm.DB, m, v string, a ml.Actor, action string, details any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Create(&modelAudit{ID: uuid.NewString(), ModelID: m, VersionID: v, ActorID: a.ID, ActorName: a.Name, Action: action, Details: string(raw), CreatedAt: time.Now().UTC()}).Error
}
func (s *ModelLifecycleStore) CreateModel(ctx context.Context, m ml.Model) (ml.Model, error) {
	requestName := m.Name
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" || utf8.RuneCountInString(m.Name) > 200 || utf8.RuneCountInString(m.Description) > 4000 || m.OwnerID == "" || m.TenantID == "" || len(m.IdempotencyKey) > 128 {
		return ml.Model{}, ml.ErrInvalid
	}
	// Display names are mutable identity metadata and are not part of the request.
	m.RequestSHA256 = ""
	if m.IdempotencyKey != "" {
		body, err := json.Marshal(struct{ Name, Description, OwnerID, TenantID string }{requestName, m.Description, m.OwnerID, m.TenantID})
		if err != nil {
			return ml.Model{}, err
		}
		digest := sha256.Sum256(body)
		m.RequestSHA256 = hex.EncodeToString(digest[:])
	}
	m.ID = uuid.NewString()
	m.Revision = 1
	m.Archived = false
	m.CreatedAt = time.Now().UTC()
	m.UpdatedAt = m.CreatedAt
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := tx
		if m.IdempotencyKey != "" {
			// The partial unique owner/key index serializes concurrent requests.
			// DO NOTHING keeps the transaction usable to read the winning row.
			insert = insert.Clauses(clause.OnConflict{DoNothing: true})
		}
		result := insert.Create(&m)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var previous ml.Model
			if err := tx.Where("owner_id = ? AND idempotency_key = ?", m.OwnerID, m.IdempotencyKey).First(&previous).Error; err != nil {
				return modelReadError(err)
			}
			if previous.RequestSHA256 != m.RequestSHA256 {
				return ml.ErrConflict
			}
			m = previous
			return nil
		}
		return writeModelAudit(tx, m.ID, "", ml.Actor{ID: m.OwnerID, Name: m.OwnerName}, "model.created", map[string]any{"name": m.Name, "revision": m.Revision})
	})
	return m, err
}
func (s *ModelLifecycleStore) GetModel(ctx context.Context, id string) (ml.Model, error) {
	var m ml.Model
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	return m, modelReadError(err)
}
func modelLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}
func (s *ModelLifecycleStore) ListModels(ctx context.Context, f ml.Filter) (ml.ModelPage, error) {
	result := ml.ModelPage{Items: []ml.Model{}}
	limit := modelLimit(f.Limit)
	query := s.db.WithContext(ctx).Where("archived = ?", f.Archived)
	if f.OwnerID != "" {
		query = query.Where("owner_id = ?", f.OwnerID)
	}
	if f.Cursor != "" {
		query = query.Where("id > ?", f.Cursor)
	}
	if f.Q != "" {
		q := "%" + strings.ToLower(f.Q) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", q, q)
	}
	if err := query.Order("id ASC").Limit(limit + 1).Find(&result.Items).Error; err != nil {
		return result, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		result.NextCursor = result.Items[limit-1].ID
	}
	return result, nil
}
func (s *ModelLifecycleStore) UpdateModel(ctx context.Context, id string, u ml.ModelUpdate, a ml.Actor) (ml.Model, error) {
	if u.Revision < 1 || a.ID == "" || (u.Name == nil && u.Description == nil && u.Archived == nil) {
		return ml.Model{}, ml.ErrInvalid
	}
	updates := map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" || utf8.RuneCountInString(name) > 200 {
			return ml.Model{}, ml.ErrInvalid
		}
		updates["name"] = name
	}
	if u.Description != nil {
		if utf8.RuneCountInString(*u.Description) > 4000 {
			return ml.Model{}, ml.ErrInvalid
		}
		updates["description"] = *u.Description
	}
	if u.Archived != nil {
		updates["archived"] = *u.Archived
	}
	var m ml.Model
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&ml.Model{}).Where("id = ? AND revision = ?", id, u.Revision).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ml.ErrConflict
		}
		if err := tx.Where("id = ?", id).First(&m).Error; err != nil {
			return err
		}
		return writeModelAudit(tx, id, "", a, "model.updated", m)
	})
	return m, err
}
func (s *ModelLifecycleStore) GetVersion(ctx context.Context, modelID, id string) (ml.Version, error) {
	var v ml.Version
	err := s.db.WithContext(ctx).Where("model_id = ? AND id = ?", modelID, id).First(&v).Error
	return v, modelReadError(err)
}
func (s *ModelLifecycleStore) ListVersions(ctx context.Context, modelID, cursor string, limit int) (ml.VersionPage, error) {
	result := ml.VersionPage{Items: []ml.Version{}}
	limit = modelLimit(limit)
	query := s.db.WithContext(ctx).Where("model_id = ?", modelID)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	if err := query.Order("id ASC").Limit(limit + 1).Find(&result.Items).Error; err != nil {
		return result, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		result.NextCursor = result.Items[limit-1].ID
	}
	return result, nil
}
func (s *ModelLifecycleStore) UpdateVersion(ctx context.Context, modelID, id string, u ml.VersionUpdate, a ml.Actor) (ml.Version, error) {
	if u.Revision < 1 || utf8.RuneCountInString(u.Description) > 4000 || a.ID == "" {
		return ml.Version{}, ml.ErrInvalid
	}
	var v ml.Version
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&ml.Version{}).Where("model_id = ? AND id = ? AND revision = ?", modelID, id, u.Revision).Updates(map[string]any{"description": u.Description, "revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ml.ErrConflict
		}
		if err := tx.Where("id = ?", id).First(&v).Error; err != nil {
			return err
		}
		return writeModelAudit(tx, modelID, id, a, "version.description.updated", map[string]any{"description": v.Description, "revision": v.Revision})
	})
	return v, err
}
func (s *ModelLifecycleStore) FindVersionRequest(ctx context.Context, modelID, creatorID, key string) (ml.Version, error) {
	var v ml.Version
	err := s.db.WithContext(ctx).Where("model_id = ? AND creator_id = ? AND idempotency_key = ?", modelID, creatorID, key).First(&v).Error
	return v, modelReadError(err)
}
