package repositories

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	mr "ray-train-platform-backend/modelrelease"
)

func (s *ModelReleaseRepository) Publish(ctx context.Context, modelID string, in mr.PublishRequest, a mr.Actor) (mr.Publication, error) {
	var out mr.Publication
	if modelID == "" || in.ReleaseID == "" || in.Revision < 0 || !mr.ValidReason(in.Reason) || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return out, mr.ErrInvalid
	}
	digest, err := releaseRequestDigest(struct {
		ReleaseID, Reason string
		Revision          int64
	}{in.ReleaseID, in.Reason, in.Revision})
	if err != nil {
		return out, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := loadReleaseModel(tx, modelID)
		if err != nil {
			return err
		}
		if !releaseOwns(m, a) {
			return mr.ErrUnauthorized
		}
		var release mr.Release
		if err := releaseVisible(tx, a).Where("id = ? AND model_id = ?", in.ReleaseID, modelID).First(&release).Error; err != nil {
			return releaseError(err)
		}
		var previous mr.History
		err = tx.Where("model_id = ? AND actor_id = ? AND idempotency_key = ?", modelID, a.ID, in.IdempotencyKey).First(&previous).Error
		if err == nil {
			if previous.RequestSHA256 != digest {
				return mr.ErrConflict
			}
			out = mr.Publication{ModelID: modelID, ReleaseID: previous.ReleaseID, VersionID: previous.VersionID, Revision: previous.Revision, ActorID: previous.ActorID, UpdatedAt: previous.CreatedAt}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if release.State != mr.Approved {
			return mr.ErrNotReady
		}
		v, e, err := releaseEvidence(tx, m, release.VersionID, release.EvaluationID, a)
		if err != nil {
			return err
		}
		if v.SHA256 != release.ModelSHA256 || e.ReportSHA256 != release.ReportSHA256 {
			return mr.ErrNotReady
		}
		var current mr.Publication
		err = tx.Where("model_id = ?", modelID).First(&current).Error
		exists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Revision != in.Revision {
			return mr.ErrConflict
		}
		now := time.Now().UTC()
		out = mr.Publication{ModelID: modelID, ReleaseID: release.ID, VersionID: release.VersionID, Revision: current.Revision + 1, ActorID: a.ID, UpdatedAt: now}
		if exists {
			result := tx.Model(&mr.Publication{}).Where("model_id = ? AND revision = ?", modelID, in.Revision).Updates(map[string]any{"release_id": out.ReleaseID, "version_id": out.VersionID, "revision": out.Revision, "actor_id": out.ActorID, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return mr.ErrConflict
			}
		} else {
			if err := tx.Create(&out).Error; err != nil {
				return err
			}
		}
		return tx.Create(&mr.History{ID: uuid.NewString(), ModelID: modelID, ReleaseID: release.ID, VersionID: release.VersionID, PreviousReleaseID: current.ReleaseID, Revision: out.Revision, ActorID: a.ID, ActorName: a.Name, Reason: in.Reason, CreatedAt: now, IdempotencyKey: in.IdempotencyKey, RequestSHA256: digest}).Error
	})
	return out, err
}
func (s *ModelReleaseRepository) GetPublication(ctx context.Context, modelID string, a mr.Actor) (mr.Publication, error) {
	var out mr.Publication
	if a.ID == "" {
		return out, mr.ErrNotFound
	}
	q := s.db.WithContext(ctx).Model(&mr.Publication{}).Select("model_publications.*").Joins("JOIN model_catalog ON model_catalog.id = model_publications.model_id AND model_catalog.archived = ?", false)
	err := q.Where("model_publications.model_id = ?", modelID).First(&out).Error
	return out, releaseError(err)
}
func (s *ModelReleaseRepository) ListHistory(ctx context.Context, modelID, cursor string, limit int, a mr.Actor) (mr.HistoryPage, error) {
	out := mr.HistoryPage{Items: []mr.History{}}
	limit = modelLimit(limit)
	if a.ID == "" {
		return out, nil
	}
	q := s.db.WithContext(ctx).Model(&mr.History{}).Select("model_publication_history.*").Joins("JOIN model_catalog ON model_catalog.id = model_publication_history.model_id AND model_catalog.archived = ?", false)
	q = q.Where("model_publication_history.model_id = ?", modelID)
	if cursor != "" {
		q = q.Where("model_publication_history.id > ?", cursor)
	}
	if err := q.Order("model_publication_history.id ASC").Limit(limit + 1).Find(&out.Items).Error; err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].ID
	}
	// Do not expose even a link to an inaccessible previous TEAM review.
	for i, item := range out.Items {
		if item.PreviousReleaseID == "" {
			continue
		}
		var count int64
		if err := releaseVisible(s.db.WithContext(ctx).Model(&mr.Release{}), a).Where("id = ?", item.PreviousReleaseID).Count(&count).Error; err != nil {
			return out, err
		}
		if count == 0 {
			copy := item
			copy.PreviousReleaseID = ""
			out.Items[i] = copy
		}
	}
	return out, nil
}
