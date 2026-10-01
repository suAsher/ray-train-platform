package repositories

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	ss "ray-train-platform-backend/storagesync"
)

type storageSyncPlanRecord struct {
	ID           string `gorm:"primaryKey"`
	Revision     int64
	SnapshotJSON string `gorm:"type:jsonb"`
	CreatedAt    time.Time
	UpdatedAt    time.Time `gorm:"autoUpdateTime:false"`
}

func (storageSyncPlanRecord) TableName() string { return "storage_sync_plans" }

type storageSyncPlanRevisionRecord struct {
	PlanID       string `gorm:"primaryKey"`
	Revision     int64  `gorm:"primaryKey"`
	SnapshotJSON string `gorm:"type:jsonb"`
	CreatedAt    time.Time
}

func (storageSyncPlanRevisionRecord) TableName() string { return "storage_sync_plan_revisions" }

type storageSyncPreviewRecord struct {
	ID           string `gorm:"primaryKey"`
	PlanID       string
	Actor        string
	State        string
	Attempt      int
	Generation   int64
	Sequence     int64
	SnapshotJSON string `gorm:"type:jsonb"`
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

func (storageSyncPreviewRecord) TableName() string { return "storage_sync_previews" }

type storageSyncRunRecord struct {
	ID             string `gorm:"primaryKey"`
	PlanID         string
	ConfigRevision int64
	RequestedBy    string
	IdempotencyKey string
	State          string
	Attempt        int
	Generation     int64
	Sequence       int64
	JobUID         string
	IdentityJSON   string `gorm:"type:jsonb"`
	SnapshotJSON   string `gorm:"type:jsonb"`
	ScheduledAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time `gorm:"autoUpdateTime:false"`
	FinishedAt     *time.Time
}

func (storageSyncRunRecord) TableName() string { return "storage_sync_runs" }

type storageSyncAttemptRecord struct {
	RunID        string `gorm:"primaryKey"`
	Attempt      int    `gorm:"primaryKey"`
	Generation   int64
	SnapshotJSON string `gorm:"type:jsonb"`
	CreatedAt    time.Time
	UpdatedAt    time.Time `gorm:"autoUpdateTime:false"`
}

func (storageSyncAttemptRecord) TableName() string { return "storage_sync_attempts" }

// These fields are deliberately absent from the API's JSON. Persist them in a
// separate internal envelope so a process restart cannot lose callback fencing,
// physical path snapshots, or proof that outstanding writes have drained.
type storageSyncPrivateSnapshot struct {
	Resolved          []ss.ResolvedMapping `json:"resolved"`
	ResolutionDigest  string               `json:"resolutionDigest"`
	SourceFingerprint string               `json:"sourceFingerprint"`
	TargetFingerprint string               `json:"targetFingerprint"`
	Attempt           int                  `json:"attempt"`
	Generation        int64                `json:"generation"`
	Sequence          int64                `json:"sequence"`
	JobUID            string               `json:"jobUID"`
	ReceiptState      string               `json:"receiptState"`
	RequestsDrained   bool                 `json:"requestsDrained"`
	StopVerified      bool                 `json:"stopVerified"`
	FilesPath         string               `json:"filesPath"`
	IdempotencyKey    string               `json:"idempotencyKey"`
	Cursor            string               `json:"cursor"`
	LastReportDigest  string               `json:"lastReportDigest"`
	WorkerID          string               `json:"workerID"`
	BaselineRef       string               `json:"baselineRef"`
	AuthorizedBy      string               `json:"authorizedBy"`
}

type storageSyncRunSnapshot struct {
	Public  ss.Run                     `json:"public"`
	Private storageSyncPrivateSnapshot `json:"private"`
}

type storageSyncPreviewSnapshot struct {
	Public  ss.Preview                 `json:"public"`
	Private storageSyncPrivateSnapshot `json:"private"`
}

func storageSyncEncodeRun(run ss.Run) (string, error) {
	private := storageSyncPrivateSnapshot{Resolved: run.Resolved, ResolutionDigest: run.ResolutionDigest,
		SourceFingerprint: run.SourceFingerprint, TargetFingerprint: run.TargetFingerprint,
		Attempt: run.Attempt, Generation: run.Generation, Sequence: run.Sequence, JobUID: run.JobUID,
		ReceiptState: run.ReceiptState, RequestsDrained: run.RequestsDrained, StopVerified: run.StopVerified,
		FilesPath: run.Files.Path, IdempotencyKey: run.IdempotencyKey, LastReportDigest: run.LastReportDigest,
		WorkerID: run.WorkerID, BaselineRef: run.BaselineRef, AuthorizedBy: run.AuthorizedBy}
	return storageSyncJSON(storageSyncRunSnapshot{Public: run, Private: private})
}

func storageSyncDecodeRun(raw string) (ss.Run, error) {
	var snapshot storageSyncRunSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return ss.Run{}, err
	}
	run, private := snapshot.Public, snapshot.Private
	run.Resolved, run.ResolutionDigest = private.Resolved, private.ResolutionDigest
	run.SourceFingerprint, run.TargetFingerprint = private.SourceFingerprint, private.TargetFingerprint
	run.Attempt, run.Generation, run.Sequence = private.Attempt, private.Generation, private.Sequence
	run.JobUID, run.ReceiptState = private.JobUID, private.ReceiptState
	run.RequestsDrained, run.StopVerified = private.RequestsDrained, private.StopVerified
	run.Files.Path, run.IdempotencyKey = private.FilesPath, private.IdempotencyKey
	run.LastReportDigest = private.LastReportDigest
	run.WorkerID, run.BaselineRef = private.WorkerID, private.BaselineRef
	run.AuthorizedBy = private.AuthorizedBy
	return run, nil
}

func storageSyncEncodePreview(preview ss.Preview) (string, error) {
	private := storageSyncPrivateSnapshot{Resolved: preview.Resolved, ResolutionDigest: preview.ResolutionDigest,
		SourceFingerprint: preview.SourceFingerprint, TargetFingerprint: preview.TargetFingerprint,
		Attempt: preview.Attempt, Generation: preview.Generation, Sequence: preview.Sequence, JobUID: preview.JobUID,
		ReceiptState: preview.ReceiptState, RequestsDrained: preview.RequestsDrained,
		FilesPath: preview.Files.Path, Cursor: preview.Cursor, LastReportDigest: preview.LastReportDigest,
		WorkerID: preview.WorkerID, BaselineRef: preview.BaselineRef}
	return storageSyncJSON(storageSyncPreviewSnapshot{Public: preview, Private: private})
}

func storageSyncDecodePreview(raw string) (ss.Preview, error) {
	var snapshot storageSyncPreviewSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return ss.Preview{}, err
	}
	preview, private := snapshot.Public, snapshot.Private
	preview.Resolved, preview.ResolutionDigest = private.Resolved, private.ResolutionDigest
	preview.SourceFingerprint, preview.TargetFingerprint = private.SourceFingerprint, private.TargetFingerprint
	preview.Attempt, preview.Generation, preview.Sequence = private.Attempt, private.Generation, private.Sequence
	preview.JobUID, preview.ReceiptState = private.JobUID, private.ReceiptState
	preview.RequestsDrained = private.RequestsDrained
	preview.Files.Path, preview.Cursor = private.FilesPath, private.Cursor
	preview.LastReportDigest = private.LastReportDigest
	preview.WorkerID, preview.BaselineRef = private.WorkerID, private.BaselineRef
	return preview, nil
}

func storageSyncJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}

func storageSyncError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ss.ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || errors.Is(err, gorm.ErrForeignKeyViolated) {
		return ss.ErrConflict
	}
	var databaseError interface{ SQLState() string }
	if errors.As(err, &databaseError) {
		switch databaseError.SQLState() {
		case "23505", "23503", "23514", "P0001":
			return ss.ErrConflict
		}
	}
	return err
}
