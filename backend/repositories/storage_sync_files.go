package repositories

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"gorm.io/gorm/clause"
	ss "ray-train-platform-backend/storagesync"
)

type storageSyncFileRecord struct {
	RunID        string `gorm:"primaryKey"`
	MappingIndex int    `gorm:"primaryKey"`
	PathKey      string `gorm:"primaryKey"`
	RelativePath string
	Attempt      int
	Generation   int64
	State        string
	SizeBytes    int64
	ErrorCode    string
}

func (storageSyncFileRecord) TableName() string { return "storage_sync_files" }

type storageSyncFileCursor struct {
	MappingIndex int    `json:"mapping"`
	PathKey      string `json:"key"`
}

func storageSyncValidFile(result ss.FileResult) bool {
	if result.MappingIndex < 0 || result.SizeBytes < 0 || len(result.ErrorCode) > 100 ||
		result.RelativePath == "" || len(result.RelativePath) > 4096 || strings.HasPrefix(result.RelativePath, "/") ||
		strings.ContainsAny(result.RelativePath, "\\\x00") ||
		(result.State != "VERIFIED" && result.State != "REUSED" && result.State != "FAILED") {
		return false
	}
	for _, segment := range strings.Split(result.RelativePath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func (tx *storageSyncTx) PutFileResults(runID string, attempt int, generation int64, results []ss.FileResult) error {
	if len(results) > 1000 {
		return ss.ErrInvalid
	}
	run, err := tx.GetRun(runID)
	if err != nil {
		return err
	}
	if run.Attempt != attempt || run.Generation != generation || run.FinishedAt != nil {
		return ss.ErrConflict
	}
	rows := make([]storageSyncFileRecord, 0, len(results))
	seen := make(map[string]bool, len(results))
	for _, result := range results {
		if !storageSyncValidFile(result) {
			return ss.ErrInvalid
		}
		digest := sha256.Sum256([]byte(result.RelativePath))
		key := hex.EncodeToString(digest[:])
		identity, err := storageSyncJSON(storageSyncFileCursor{MappingIndex: result.MappingIndex, PathKey: key})
		if err != nil || seen[identity] {
			return ss.ErrInvalid
		}
		seen[identity] = true
		rows = append(rows, storageSyncFileRecord{RunID: runID, Attempt: attempt, Generation: generation, PathKey: key,
			MappingIndex: result.MappingIndex, RelativePath: result.RelativePath, State: result.State,
			SizeBytes: result.SizeBytes, ErrorCode: result.ErrorCode})
	}
	if len(rows) == 0 {
		return nil
	}
	result := tx.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}, {Name: "mapping_index"}, {Name: "path_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"attempt", "generation", "state", "size_bytes", "error_code"}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "storage_sync_files.relative_path = excluded.relative_path"}}}}).Create(&rows)
	if result.Error != nil {
		return storageSyncError(result.Error)
	}
	if result.RowsAffected != int64(len(rows)) {
		return ss.ErrConflict
	}
	return nil
}

func (r *StorageSyncRepository) ListRunFiles(ctx context.Context, runID, cursor string, limit int) (ss.FilePage, error) {
	if limit < 1 || limit > 1000 {
		return ss.FilePage{}, ss.ErrInvalid
	}
	if _, err := r.GetRun(ctx, runID); err != nil {
		return ss.FilePage{}, err
	}
	query := r.db.WithContext(ctx).Where("run_id = ?", runID).Order("mapping_index, path_key").Limit(limit + 1)
	if cursor != "" {
		position, err := storageSyncDecodeFileCursor(cursor)
		if err != nil {
			return ss.FilePage{}, err
		}
		query = query.Where("mapping_index > ? OR (mapping_index = ? AND path_key > ?)", position.MappingIndex, position.MappingIndex, position.PathKey)
	}
	var rows []storageSyncFileRecord
	if err := query.Find(&rows).Error; err != nil {
		return ss.FilePage{}, err
	}
	page := ss.FilePage{Items: make([]ss.FileResult, 0, limit)}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		raw, err := json.Marshal(storageSyncFileCursor{MappingIndex: last.MappingIndex, PathKey: last.PathKey})
		if err != nil {
			return ss.FilePage{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	for _, row := range rows {
		page.Items = append(page.Items, ss.FileResult{MappingIndex: row.MappingIndex, RelativePath: row.RelativePath,
			State: row.State, SizeBytes: row.SizeBytes, ErrorCode: row.ErrorCode})
	}
	return page, nil
}

func storageSyncDecodeFileCursor(cursor string) (storageSyncFileCursor, error) {
	var position storageSyncFileCursor
	if len(cursor) > 8192 {
		return position, ss.ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(raw, &position) != nil || position.MappingIndex < 0 || len(position.PathKey) != 64 {
		return storageSyncFileCursor{}, ss.ErrInvalid
	}
	if _, err := hex.DecodeString(position.PathKey); err != nil {
		return storageSyncFileCursor{}, ss.ErrInvalid
	}
	return position, nil
}
