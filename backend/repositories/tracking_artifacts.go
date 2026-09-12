package repositories

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	artifacts "ray-train-platform-backend/trackingartifacts"
)

type TrackingArtifactRecord struct {
	ID              string `gorm:"primaryKey"`
	RunID           string
	TenantID        string
	UserID          string
	IdempotencyHash string
	Name            string
	SizeBytes       int64
	SHA256          string
	State           string
	PartSizeBytes   int64
	TotalParts      int
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

func (TrackingArtifactRecord) TableName() string { return "mlflow_tracking_artifacts" }

type TrackingArtifactPartRecord struct {
	ArtifactID string `gorm:"primaryKey"`
	PartIndex  int    `gorm:"primaryKey"`
	SizeBytes  int64
	SHA256     string
}

func (TrackingArtifactPartRecord) TableName() string { return "mlflow_tracking_artifact_parts" }

type TrackingArtifactStore struct{ db *gorm.DB }

func NewTrackingArtifactStore(db *gorm.DB) *TrackingArtifactStore {
	return &TrackingArtifactStore{db: db}
}
func artifactRecord(v artifacts.Record) TrackingArtifactRecord {
	return TrackingArtifactRecord{ID: v.ID, RunID: v.Scope.RunID, TenantID: v.Scope.TenantID, UserID: v.Scope.OwnerID, IdempotencyHash: v.IdempotencyHash, Name: v.Name, SizeBytes: v.SizeBytes, SHA256: v.SHA256, State: v.State, PartSizeBytes: v.PartSizeBytes, TotalParts: v.TotalParts, ExpiresAt: v.ExpiresAt, CreatedAt: v.CreatedAt}
}
func artifactValue(v TrackingArtifactRecord) artifacts.Record {
	return artifacts.Record{Artifact: artifacts.Artifact{ID: v.ID, RunID: v.RunID, Name: v.Name, SizeBytes: v.SizeBytes, SHA256: v.SHA256, State: v.State, PartSizeBytes: v.PartSizeBytes, TotalParts: v.TotalParts, ExpiresAt: v.ExpiresAt, CreatedAt: v.CreatedAt, UploadedParts: []artifacts.Part{}}, Scope: artifacts.Scope{TenantID: v.TenantID, OwnerID: v.UserID, RunID: v.RunID}, IdempotencyHash: v.IdempotencyHash}
}
func artifactScope(db *gorm.DB, scope artifacts.Scope) *gorm.DB {
	return db.Where("tenant_id = ? AND user_id = ? AND run_id = ?", scope.TenantID, scope.OwnerID, scope.RunID)
}
func artifactError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return artifacts.ErrNotFound
	}
	for _, known := range []error{artifacts.ErrNotFound, artifacts.ErrConflict, artifacts.ErrQuota, artifacts.ErrInvalid} {
		if errors.Is(err, known) {
			return known
		}
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "55P03", "40P01", "40001":
			return artifacts.ErrConflict
		}
	}
	return artifacts.ErrUnavailable
}
func artifactTransaction(tx *gorm.DB) error {
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec("SET LOCAL lock_timeout = '5s'").Error
	}
	return nil
}
func loadArtifactParts(tx *gorm.DB, v artifacts.Record) (artifacts.Record, error) {
	var rows []TrackingArtifactPartRecord
	if err := tx.Where("artifact_id = ?", v.ID).Order("part_index ASC").Find(&rows).Error; err != nil {
		return artifacts.Record{}, err
	}
	v.UploadedParts = make([]artifacts.Part, 0, len(rows))
	for _, p := range rows {
		v.UploadedParts = append(v.UploadedParts, artifacts.Part{Index: p.PartIndex, SizeBytes: p.SizeBytes, SHA256: p.SHA256})
	}
	return v, nil
}
func (s *TrackingArtifactStore) Reserve(ctx context.Context, input artifacts.Record) (artifacts.Record, error) {
	var result artifacts.Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := artifactTransaction(tx); err != nil {
			return err
		}
		// Only admission takes this short owner-scoped lock. Cancellation can only
		// reduce the aggregate and needs no owner lock during object cleanup.
		sum := sha256.Sum256([]byte("mlflow-artifact-budget\x00" + input.Scope.TenantID + "\x00" + input.Scope.OwnerID))
		key := int64(binary.BigEndian.Uint64(sum[:8]))
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error; err != nil {
				return err
			}
		}
		var existing TrackingArtifactRecord
		err := tx.Where("tenant_id = ? AND user_id = ? AND idempotency_hash = ?", input.Scope.TenantID, input.Scope.OwnerID, input.IdempotencyHash).Take(&existing).Error
		if err == nil {
			if existing.RunID != input.Scope.RunID || existing.Name != input.Name || existing.SizeBytes != input.SizeBytes || existing.SHA256 != input.SHA256 {
				return artifacts.ErrConflict
			}
			result, err = loadArtifactParts(tx, artifactValue(existing))
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var budget struct {
			SizeBytes int64
			Pending   int64
		}
		if err := tx.Raw(`SELECT COALESCE(SUM(size_bytes),0) AS size_bytes, COUNT(*) FILTER (WHERE state='PENDING') AS pending FROM mlflow_tracking_artifacts WHERE tenant_id=? AND user_id=? AND state <> 'CANCELLED'`, input.Scope.TenantID, input.Scope.OwnerID).Scan(&budget).Error; err != nil {
			return err
		}
		if budget.SizeBytes > artifacts.OwnerBudgetBytes-input.SizeBytes || budget.Pending >= artifacts.MaxPending {
			return artifacts.ErrQuota
		}
		record := artifactRecord(input)
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		result = artifactValue(record)
		return nil
	})
	return result, artifactError(err)
}
func (s *TrackingArtifactStore) Get(ctx context.Context, scope artifacts.Scope, id string) (artifacts.Record, error) {
	var record TrackingArtifactRecord
	tx := s.db.WithContext(ctx)
	if err := artifactScope(tx, scope).Where("id = ?", id).Take(&record).Error; err != nil {
		return artifacts.Record{}, artifactError(err)
	}
	v, err := loadArtifactParts(tx, artifactValue(record))
	return v, artifactError(err)
}
func (s *TrackingArtifactStore) List(ctx context.Context, scope artifacts.Scope, cursor string, limit int) ([]artifacts.Record, error) {
	if limit < 1 || limit > 101 {
		return nil, artifacts.ErrInvalid
	}
	var rows []TrackingArtifactRecord
	tx := s.db.WithContext(ctx)
	query := artifactScope(tx, scope)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	if err := query.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, artifactError(err)
	}
	result := make([]artifacts.Record, 0, len(rows))
	for _, row := range rows {
		v, err := loadArtifactParts(tx, artifactValue(row))
		if err != nil {
			return nil, artifactError(err)
		}
		result = append(result, v)
	}
	return result, nil
}
func (s *TrackingArtifactStore) Mutate(ctx context.Context, scope artifacts.Scope, id string, fn func(artifacts.Record) (artifacts.Record, error)) (artifacts.Record, error) {
	var result artifacts.Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := artifactTransaction(tx); err != nil {
			return err
		}
		var row TrackingArtifactRecord
		if err := artifactScope(tx, scope).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		before, err := loadArtifactParts(tx, artifactValue(row))
		if err != nil {
			return err
		}
		input := before
		input.UploadedParts = append([]artifacts.Part{}, before.UploadedParts...)
		after, err := fn(input)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return artifacts.ErrUnavailable
		}
		if !sameArtifactDeclaration(before, after) || (before.State != "PENDING" && after.State != before.State) || (after.State != "PENDING" && after.State != "READY" && after.State != "CANCELLED") {
			return artifacts.ErrConflict
		}
		known := make(map[int]artifacts.Part, len(before.UploadedParts))
		for _, p := range before.UploadedParts {
			known[p.Index] = p
		}
		seen := map[int]bool{}
		for _, p := range after.UploadedParts {
			if seen[p.Index] {
				return artifacts.ErrConflict
			}
			seen[p.Index] = true
			if old, ok := known[p.Index]; ok {
				if old != p {
					return artifacts.ErrConflict
				}
				continue
			}
			if before.State != "PENDING" {
				return artifacts.ErrConflict
			}
			record := TrackingArtifactPartRecord{ArtifactID: id, PartIndex: p.Index, SizeBytes: p.SizeBytes, SHA256: p.SHA256}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
		}
		for index := range known {
			if !seen[index] {
				return artifacts.ErrConflict
			}
		}
		if after.State != before.State {
			if err := artifactScope(tx.Model(&TrackingArtifactRecord{}), scope).Where("id = ?", id).Update("state", after.State).Error; err != nil {
				return err
			}
		}
		result = after
		return nil
	})
	return result, artifactError(err)
}
func sameArtifactDeclaration(a, b artifacts.Record) bool {
	return a.ID == b.ID && a.Scope == b.Scope && a.RunID == b.RunID && a.IdempotencyHash == b.IdempotencyHash && a.Name == b.Name && a.SizeBytes == b.SizeBytes && a.SHA256 == b.SHA256 && a.PartSizeBytes == b.PartSizeBytes && a.TotalParts == b.TotalParts && a.ExpiresAt.Equal(b.ExpiresAt) && a.CreatedAt.Equal(b.CreatedAt)
}
