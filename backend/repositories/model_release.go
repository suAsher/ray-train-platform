package repositories

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelrelease"
)

type ModelReleaseRepository struct{ db *gorm.DB }

func NewModelReleaseRepository(db *gorm.DB) *ModelReleaseRepository {
	return &ModelReleaseRepository{db: db}
}

var _ mr.Repository = (*ModelReleaseRepository)(nil)

type modelReleaseAudit struct {
	ID        string `gorm:"primaryKey"`
	ReleaseID string
	ActorID   string
	Action    string
	Reason    string
	CreatedAt time.Time
}

func (modelReleaseAudit) TableName() string { return "model_release_audits" }
func releaseError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return mr.ErrNotFound
	}
	return err
}
func releaseDigest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func releaseRequestDigest(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return releaseDigest(raw), nil
}
func releaseVisible(q *gorm.DB, a mr.Actor) *gorm.DB {
	if a.ID == "" {
		return q.Where("1=0")
	}
	if a.SuperAdmin {
		return q
	}
	return q.Where("model_releases.dataset_visibility = ? OR (model_releases.dataset_visibility = ? AND model_releases.dataset_tenant_id = ?)", me.Public, me.Team, a.TenantID)
}
func loadReleaseModel(tx *gorm.DB, id string) (ml.Model, error) {
	var m ml.Model
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&m).Error
	return m, releaseError(err)
}
func releaseOwns(m ml.Model, a mr.Actor) bool {
	return a.ID != "" && (a.ID == m.OwnerID || a.SuperAdmin)
}

// All writers lock the catalog row first, serializing publication with archive.
// READY model bytes and terminal accepted evaluation reports are immutable in SQL.
func releaseEvidence(tx *gorm.DB, m ml.Model, versionID, evaluationID string, a mr.Actor) (ml.Version, modelEvaluationRecord, error) {
	var v ml.Version
	var e modelEvaluationRecord
	if m.Archived {
		return v, e, mr.ErrNotReady
	}
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND model_id = ?", versionID, m.ID).First(&v).Error; err != nil {
		return v, e, releaseError(err)
	}
	q := evaluationVisible(tx, a.TenantID, a.SuperAdmin)
	if err := q.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", evaluationID).First(&e).Error; err != nil {
		return v, e, releaseError(err)
	}
	var snapshot me.Evaluation
	if err := json.Unmarshal([]byte(e.SnapshotJSON), &snapshot); err != nil {
		return v, e, err
	}
	if v.State != ml.Ready || len(v.SHA256) != 64 || e.ModelID != m.ID || e.VersionID != v.ID || snapshot.ModelSHA256 != v.SHA256 || e.State != me.Succeeded || e.ReportState != me.ReportValid || len(e.ReportSHA256) != 64 {
		return v, e, mr.ErrNotReady
	}
	frozen, err := e.evaluation()
	if err != nil {
		return v, e, mr.ErrNotReady
	}
	_, reportDigest, err := me.ValidateReport(json.RawMessage(e.ReportJSON), frozen)
	if err != nil || reportDigest != e.ReportSHA256 {
		return v, e, mr.ErrNotReady
	}
	return v, e, nil
}
func releaseAudit(tx *gorm.DB, r mr.Release, a mr.Actor, action, reason string) error {
	return tx.Create(&modelReleaseAudit{ID: uuid.NewString(), ReleaseID: r.ID, ActorID: a.ID, Action: action, Reason: reason, CreatedAt: time.Now().UTC()}).Error
}
func (s *ModelReleaseRepository) CreateRequest(ctx context.Context, in mr.Request, a mr.Actor) (mr.Release, error) {
	var out mr.Release
	if err := mr.ValidateRequest(in); err != nil {
		return out, err
	}
	if a.ID == "" || a.TenantID == "" {
		return out, mr.ErrUnauthorized
	}
	digest, err := releaseRequestDigest(struct{ ModelID, VersionID, EvaluationID, Reason string }{in.ModelID, in.VersionID, in.EvaluationID, in.Reason})
	if err != nil {
		return out, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := loadReleaseModel(tx, in.ModelID)
		if err != nil {
			return err
		}
		if !releaseOwns(m, a) {
			return mr.ErrUnauthorized
		}
		var old mr.Release
		err = releaseVisible(tx, a).Where("applicant_id = ? AND tenant_id = ? AND idempotency_key = ?", a.ID, a.TenantID, in.IdempotencyKey).First(&old).Error
		if err == nil {
			if old.RequestSHA256 != digest {
				return mr.ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		v, e, err := releaseEvidence(tx, m, in.VersionID, in.EvaluationID, a)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		out = mr.Release{ID: uuid.NewString(), ModelID: m.ID, VersionID: v.ID, ModelSHA256: v.SHA256, ModelOwnerID: m.OwnerID, EvaluationID: e.ID, ReportSHA256: e.ReportSHA256, DatasetVisibility: e.DatasetVisibility, DatasetTenantID: e.DatasetTenantID, ApplicantID: a.ID, ApplicantName: a.Name, TenantID: a.TenantID, Reason: in.Reason, State: mr.Pending, Revision: 1, CreatedAt: now, UpdatedAt: now, IdempotencyKey: in.IdempotencyKey, RequestSHA256: digest}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&out)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			if err := releaseVisible(tx, a).Where("applicant_id = ? AND tenant_id = ? AND idempotency_key = ?", a.ID, a.TenantID, in.IdempotencyKey).First(&old).Error; err != nil {
				return releaseError(err)
			}
			if old.RequestSHA256 != digest {
				return mr.ErrConflict
			}
			out = old
			return nil
		}
		return releaseAudit(tx, out, a, "requested", in.Reason)
	})
	return out, err
}
func (s *ModelReleaseRepository) GetRelease(ctx context.Context, id string, a mr.Actor) (mr.Release, error) {
	var out mr.Release
	err := releaseVisible(s.db.WithContext(ctx), a).Where("id = ?", id).First(&out).Error
	return out, releaseError(err)
}
func (s *ModelReleaseRepository) ListReleases(ctx context.Context, f mr.Filter, a mr.Actor) (mr.Page, error) {
	out := mr.Page{Items: []mr.Release{}}
	q := releaseVisible(s.db.WithContext(ctx).Model(&mr.Release{}), a)
	for column, value := range map[string]string{"model_id": f.ModelID, "version_id": f.VersionID, "state": f.State} {
		if value != "" {
			q = q.Where(column+" = ?", value)
		}
	}
	if f.Cursor != "" {
		q = q.Where("id > ?", f.Cursor)
	}
	limit := modelLimit(f.Limit)
	if err := q.Order("id ASC").Limit(limit + 1).Find(&out.Items).Error; err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].ID
	}
	return out, nil
}
func (s *ModelReleaseRepository) Decide(ctx context.Context, id string, in mr.Decision, a mr.Actor) (mr.Release, error) {
	var out mr.Release
	if in.Revision < 1 || !mr.ValidReason(in.Reason) {
		return out, mr.ErrInvalid
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var peek mr.Release
		if err := releaseVisible(tx, a).Where("id = ?", id).First(&peek).Error; err != nil {
			return releaseError(err)
		}
		m, err := loadReleaseModel(tx, peek.ModelID)
		if err != nil {
			return err
		}
		if err := releaseVisible(tx, a).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&out).Error; err != nil {
			return releaseError(err)
		}
		if !mr.CanReview(out, a) || a.ID == m.OwnerID {
			return mr.ErrUnauthorized
		}
		if out.State != mr.Pending || out.Revision != in.Revision {
			return mr.ErrConflict
		}
		if in.Approve {
			v, e, err := releaseEvidence(tx, m, out.VersionID, out.EvaluationID, a)
			if err != nil {
				return err
			}
			if v.SHA256 != out.ModelSHA256 || e.ReportSHA256 != out.ReportSHA256 {
				return mr.ErrNotReady
			}
		}
		now := time.Now().UTC()
		state, action := mr.Rejected, "rejected"
		if in.Approve {
			state, action = mr.Approved, "approved"
		}
		result := tx.Model(&mr.Release{}).Where("id = ? AND revision = ? AND state = ?", id, in.Revision, mr.Pending).Updates(map[string]any{"state": state, "reviewer_id": a.ID, "reviewer_name": a.Name, "review_reason": in.Reason, "reviewed_at": now, "revision": gorm.Expr("revision + 1"), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return mr.ErrConflict
		}
		if err := tx.Where("id = ?", id).First(&out).Error; err != nil {
			return err
		}
		return releaseAudit(tx, out, a, action, in.Reason)
	})
	return out, err
}
