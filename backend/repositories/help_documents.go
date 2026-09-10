package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var ErrHelpConflict = errors.New("help document version conflict")
var ErrHelpNotFound = errors.New("help document not found")

// Snapshots are JSON text to keep identical semantics in PostgreSQL and SQLite tests.
type HelpDocumentRecord struct {
	ID            string `gorm:"primaryKey"`
	Version       int64
	DraftJSON     string
	PublishedJSON string
}

func (HelpDocumentRecord) TableName() string { return "help_documents" }

type HelpRevisionRecord struct {
	DocumentID   string `gorm:"primaryKey"`
	Version      int64  `gorm:"primaryKey;autoIncrement:false"`
	SnapshotJSON string
}

func (HelpRevisionRecord) TableName() string { return "help_document_revisions" }

func (r *GormRepository) ListHelpDocuments(ctx context.Context, admin bool) ([]domain.HelpDocument, error) {
	var records []HelpDocumentRecord
	q := r.db.WithContext(ctx)
	if !admin {
		q = q.Where("published_json <> ?", "")
	}
	if err := q.Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.HelpDocument, 0, len(records))
	for _, record := range records {
		raw := record.PublishedJSON
		if admin {
			raw = record.DraftJSON
		}
		var d domain.HelpDocument
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].SortOrder != items[j].SortOrder {
			return items[i].SortOrder < items[j].SortOrder
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func insertHelp(tx *gorm.DB, d domain.HelpDocument, actor string, published bool) (domain.HelpDocument, bool, error) {
	if err := d.Validate(); err != nil {
		return d, false, err
	}
	d.Version = 1
	d.PublishedVersion = 0
	d.UpdatedAt = time.Now().UTC()
	d.UpdatedBy = actor
	d.Action = "create"
	if published {
		d.PublishedVersion = 1
		d.Action = "seed"
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return d, false, err
	}
	rec := HelpDocumentRecord{ID: d.ID, Version: 1, DraftJSON: string(raw)}
	if published {
		rec.PublishedJSON = string(raw)
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
	if result.Error != nil {
		return d, false, result.Error
	}
	if result.RowsAffected == 0 {
		return d, false, nil
	}
	err = tx.Create(&HelpRevisionRecord{DocumentID: d.ID, Version: 1, SnapshotJSON: string(raw)}).Error
	return d, true, err
}

func (r *GormRepository) CreateHelpDocument(ctx context.Context, d domain.HelpDocument, actor string) (domain.HelpDocument, error) {
	var out domain.HelpDocument
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var inserted bool
		var err error
		out, inserted, err = insertHelp(tx, d, actor, false)
		if err != nil {
			return err
		}
		if !inserted {
			return ErrHelpConflict
		}
		return nil
	})
	return out, err
}

func (r *GormRepository) SeedHelpDocuments(ctx context.Context, docs []domain.HelpDocument) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, d := range docs {
			if _, inserted, err := insertHelp(tx, d, "platform-seed", true); err != nil {
				return err
			} else if !inserted {
				if err := refreshSeedHelp(tx, d); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func refreshSeedHelp(tx *gorm.DB, seed domain.HelpDocument) error {
	var rec HelpDocumentRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", seed.ID).First(&rec).Error; err != nil {
		return err
	}
	var current domain.HelpDocument
	if err := json.Unmarshal([]byte(rec.DraftJSON), &current); err != nil {
		return err
	}
	if current.UpdatedBy != "platform-seed" || sameHelpContent(current, seed) {
		return nil
	}
	seed.Version = rec.Version + 1
	seed.PublishedVersion = seed.Version
	seed.UpdatedAt = time.Now().UTC()
	seed.UpdatedBy = "platform-seed"
	seed.Action = "seed"
	raw, err := json.Marshal(seed)
	if err != nil {
		return err
	}
	result := tx.Model(&HelpDocumentRecord{}).Where("id = ? AND version = ?", seed.ID, rec.Version).Updates(map[string]any{
		"version": rec.Version + 1, "draft_json": string(raw), "published_json": string(raw),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrHelpConflict
	}
	return tx.Create(&HelpRevisionRecord{DocumentID: seed.ID, Version: seed.Version, SnapshotJSON: string(raw)}).Error
}

func sameHelpContent(left, right domain.HelpDocument) bool {
	return left.Title == right.Title && left.Category == right.Category && left.SortOrder == right.SortOrder && left.Markdown == right.Markdown
}

func (r *GormRepository) ChangeHelpDocument(ctx context.Context, id string, expected int64, action string, restore int64, input *domain.HelpDocument, actor string) (domain.HelpDocument, error) {
	var out domain.HelpDocument
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rec HelpDocumentRecord
		if err := tx.Where("id = ?", id).First(&rec).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrHelpNotFound
			}
			return err
		}
		if expected < 1 || rec.Version != expected {
			return ErrHelpConflict
		}
		if err := json.Unmarshal([]byte(rec.DraftJSON), &out); err != nil {
			return err
		}
		switch action {
		case "save":
			if input == nil {
				return errors.New("missing help draft")
			}
			out.Title = input.Title
			out.Category = input.Category
			out.SortOrder = input.SortOrder
			out.Markdown = input.Markdown
		case "restore":
			var revision HelpRevisionRecord
			if err := tx.Where("document_id = ? AND version = ?", id, restore).First(&revision).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrHelpNotFound
				}
				return err
			}
			var old domain.HelpDocument
			if err := json.Unmarshal([]byte(revision.SnapshotJSON), &old); err != nil {
				return err
			}
			out.Title = old.Title
			out.Category = old.Category
			out.SortOrder = old.SortOrder
			out.Markdown = old.Markdown
		case "publish":
			out.PublishedVersion = expected + 1
		case "unpublish":
			out.PublishedVersion = 0
		default:
			return errors.New("invalid help document action")
		}
		if err := out.Validate(); err != nil {
			return err
		}
		out.Version = expected + 1
		out.UpdatedAt = time.Now().UTC()
		out.UpdatedBy = actor
		out.Action = action
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		published := rec.PublishedJSON
		if action == "publish" {
			published = string(raw)
		}
		if action == "unpublish" {
			published = ""
		}
		result := tx.Model(&HelpDocumentRecord{}).Where("id = ? AND version = ?", id, expected).Updates(map[string]any{"version": out.Version, "draft_json": string(raw), "published_json": published})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrHelpConflict
		}
		return tx.Create(&HelpRevisionRecord{DocumentID: id, Version: out.Version, SnapshotJSON: string(raw)}).Error
	})
	return out, err
}

func (r *GormRepository) HelpDocumentHistory(ctx context.Context, id string) ([]domain.HelpDocument, error) {
	var records []HelpRevisionRecord
	if err := r.db.WithContext(ctx).Where("document_id = ?", id).Order("version DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrHelpNotFound
	}
	items := make([]domain.HelpDocument, 0, len(records))
	for _, record := range records {
		var d domain.HelpDocument
		if err := json.Unmarshal([]byte(record.SnapshotJSON), &d); err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, nil
}
