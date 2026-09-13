package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	ms "ray-train-platform-backend/modelserving"
	"time"
)

type ModelServingRepository struct{ db *gorm.DB }

func NewModelServingRepository(db *gorm.DB) *ModelServingRepository {
	return &ModelServingRepository{db: db}
}

var _ ms.Store = (*ModelServingRepository)(nil)

type modelServingContractRecord struct {
	ID                      string `gorm:"primaryKey"`
	Name, OwnerID, TenantID string
	SnapshotJSON            string `gorm:"type:jsonb"`
	Active                  bool
	Revision                int64
	CreatedAt               time.Time
}

func (modelServingContractRecord) TableName() string { return "model_serving_contracts" }

type modelServingDeploymentRecord struct {
	ID                                                                        string `gorm:"primaryKey"`
	Name, ReleaseID, ModelID, VersionID, ContractID, OwnerID, TenantID, JobID string
	SnapshotJSON                                                              string `gorm:"type:jsonb"`
	JobSpecJSON                                                               string `gorm:"type:jsonb"`
	State, Error                                                              string
	Revision                                                                  int64
	IdempotencyKey, RequestSHA256                                             string
	ExpiresAt, CreatedAt, UpdatedAt                                           time.Time
}

func (modelServingDeploymentRecord) TableName() string { return "model_serving_deployments" }
func servingReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ms.ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ms.ErrConflict
	}
	return err
}
func (r modelServingContractRecord) contract() (ms.Contract, error) {
	var c ms.Contract
	if err := json.Unmarshal([]byte(r.SnapshotJSON), &c); err != nil {
		return c, err
	}
	c.ID, c.Active, c.Revision, c.CreatedAt = r.ID, r.Active, r.Revision, r.CreatedAt
	return c, nil
}
func (r modelServingDeploymentRecord) deployment() (ms.Deployment, error) {
	var d ms.Deployment
	if err := json.Unmarshal([]byte(r.SnapshotJSON), &d); err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(r.JobSpecJSON), &d.JobSpec); err != nil {
		return d, err
	}
	d.ID, d.JobID, d.State, d.Error, d.Revision = r.ID, r.JobID, r.State, r.Error, r.Revision
	d.ExpiresAt, d.CreatedAt, d.UpdatedAt = r.ExpiresAt, r.CreatedAt, r.UpdatedAt
	d.IdempotencyKey, d.RequestSHA256 = r.IdempotencyKey, r.RequestSHA256
	return d, nil
}
func (s *ModelServingRepository) CreateContract(ctx context.Context, c ms.Contract) (ms.Contract, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	c.Active, c.Revision, c.CreatedAt = true, 1, time.Now().UTC().Truncate(time.Microsecond)
	if err := ms.ValidateContract(c); err != nil {
		return ms.Contract{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ms.Contract{}, err
	}
	r := modelServingContractRecord{ID: c.ID, Name: c.Name, OwnerID: c.OwnerID, TenantID: c.TenantID, SnapshotJSON: string(raw), Active: true, Revision: 1, CreatedAt: c.CreatedAt}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
	if result.Error != nil {
		return ms.Contract{}, servingReadError(result.Error)
	}
	if result.RowsAffected != 1 {
		return ms.Contract{}, ms.ErrConflict
	}
	return c, nil
}
func (s *ModelServingRepository) GetContract(ctx context.Context, id string) (ms.Contract, error) {
	var r modelServingContractRecord
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&r).Error; err != nil {
		return ms.Contract{}, servingReadError(err)
	}
	return r.contract()
}
func (s *ModelServingRepository) ListContracts(ctx context.Context, includeInactive bool) ([]ms.Contract, error) {
	query := s.db.WithContext(ctx)
	if !includeInactive {
		query = query.Where("active = ?", true)
	}
	var records []modelServingContractRecord
	if err := query.Order("created_at DESC, id DESC").Limit(200).Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]ms.Contract, 0, len(records))
	for _, r := range records {
		c, err := r.contract()
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, nil
}
func (s *ModelServingRepository) SetContractActive(ctx context.Context, id string, active bool, revision int64) (ms.Contract, error) {
	if revision < 1 {
		return ms.Contract{}, ms.ErrInvalid
	}
	var c ms.Contract
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&modelServingContractRecord{}).Where("id = ? AND revision = ?", id, revision).Updates(map[string]any{"active": active, "revision": gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ms.ErrConflict
		}
		var r modelServingContractRecord
		if err := tx.Where("id = ?", id).First(&r).Error; err != nil {
			return err
		}
		var err error
		c, err = r.contract()
		return err
	})
	return c, err
}
func (s *ModelServingRepository) GetDeployment(ctx context.Context, id string) (ms.Deployment, error) {
	var r modelServingDeploymentRecord
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&r).Error; err != nil {
		return ms.Deployment{}, servingReadError(err)
	}
	return r.deployment()
}
func (s *ModelServingRepository) GetDeploymentByJobID(ctx context.Context, jobID string) (ms.Deployment, error) {
	var r modelServingDeploymentRecord
	if err := s.db.WithContext(ctx).Where("job_id = ?", jobID).First(&r).Error; err != nil {
		return ms.Deployment{}, servingReadError(err)
	}
	return r.deployment()
}
func (s *ModelServingRepository) FindDeploymentRequest(ctx context.Context, tenant, owner, key string) (ms.Deployment, error) {
	if tenant == "" || owner == "" || key == "" {
		return ms.Deployment{}, ms.ErrInvalid
	}
	var r modelServingDeploymentRecord
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND owner_id = ? AND idempotency_key = ?", tenant, owner, key).First(&r).Error; err != nil {
		return ms.Deployment{}, servingReadError(err)
	}
	return r.deployment()
}
func (s *ModelServingRepository) ListDeployments(ctx context.Context, f ms.Filter) (ms.DeploymentPage, error) {
	page := ms.DeploymentPage{Items: []ms.Deployment{}}
	query := s.db.WithContext(ctx)
	for column, value := range map[string]string{"model_id": f.ModelID, "owner_id": f.OwnerID, "state": f.State} {
		if value != "" {
			query = query.Where(column+" = ?", value)
		}
	}
	if f.Cursor != "" {
		query = query.Where("id > ?", f.Cursor)
	}
	limit := modelLimit(f.Limit)
	var records []modelServingDeploymentRecord
	if err := query.Order("id ASC").Limit(limit + 1).Find(&records).Error; err != nil {
		return page, err
	}
	if len(records) > limit {
		records = records[:limit]
		page.NextCursor = records[limit-1].ID
	}
	for _, r := range records {
		d, err := r.deployment()
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, d)
	}
	return page, nil
}
func (s *ModelServingRepository) GetPendingDeployments(ctx context.Context, limit int) ([]ms.Deployment, error) {
	var records []modelServingDeploymentRecord
	if err := s.db.WithContext(ctx).Where("state IN ?", []string{ms.Creating, ms.Submitted, ms.Ready, ms.Stopping}).Order("updated_at ASC, id ASC").Limit(modelLimit(limit)).Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]ms.Deployment, 0, len(records))
	for _, r := range records {
		d, err := r.deployment()
		if err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, nil
}
func writeServingAudit(tx *gorm.DB, id, actor, action string, now time.Time) error {
	return tx.Exec("INSERT INTO model_serving_audits (id,deployment_id,actor_id,action,created_at) VALUES (?,?,?,?,?)", uuid.NewString(), id, actor, action, now).Error
}
