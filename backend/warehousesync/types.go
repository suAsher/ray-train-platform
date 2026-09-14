// Package warehousesync coordinates durable, user-authorized model exports.
package warehousesync

import (
	"context"
	"errors"
	"io"
	"time"

	fw "ray-train-platform-backend/functionwarehouse"
)

const (
	WaitingSource = "WAITING_SOURCE"
	Queued        = "QUEUED"
	Uploading     = "UPLOADING"
	Registering   = "REGISTERING"
	Succeeded     = "SUCCEEDED"
	Failed        = "FAILED"
	WaitingReauth = "WAITING_REAUTH"
	Unknown       = "UNKNOWN"
	Canceled      = "CANCELED"
)

var (
	ErrInvalid      = errors.New("invalid warehouse sync request")
	ErrForbidden    = errors.New("warehouse sync access denied")
	ErrNotFound     = errors.New("warehouse sync not found")
	ErrConflict     = errors.New("warehouse sync operation conflict")
	ErrPending      = errors.New("warehouse sync source pending")
	ErrSourceFailed = errors.New("automatic sync requires successful training")
	ErrQuota        = errors.New("too many pending warehouse syncs")
)

type Actor struct{ ID, Name, TenantID string }

// Request describes one deliberate export. The API supplies a fresh key for a
// new action and reuses it only while retrying that same submission.
type Request struct {
	JobID          string         `json:"jobId"`
	Environment    fw.Environment `json:"environment"`
	WarehouseID    string         `json:"warehouseId"`
	ModelTypeID    string         `json:"modelTypeId"`
	Version        string         `json:"version"`
	Paths          []string       `json:"paths"`
	Automatic      bool           `json:"automatic"`
	IdempotencyKey string         `json:"-"`
}

type File struct {
	ModelID   string `json:"modelId"`
	VersionID string `json:"versionId"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type SourceIdentity struct{ JobID, RunID, ExperimentID string }

type Operation struct {
	ID              string         `json:"id" gorm:"primaryKey"`
	OwnerID         string         `json:"ownerId"`
	OwnerName       string         `json:"ownerName"`
	TenantID        string         `json:"tenantId"`
	JobID           string         `json:"jobId"`
	Environment     fw.Environment `json:"environment"`
	WarehouseID     string         `json:"warehouseId"`
	ModelTypeID     string         `json:"modelTypeId"`
	Version         string         `json:"version"`
	Paths           []string       `json:"paths" gorm:"serializer:json;type:jsonb"`
	Automatic       bool           `json:"automatic"`
	State           string         `json:"state"`
	Message         string         `json:"message"`
	Files           []File         `json:"files" gorm:"serializer:json;type:jsonb"`
	RunID           string         `json:"runId,omitempty"`
	ExperimentID    string         `json:"experimentId,omitempty"`
	TargetVersionID string         `json:"targetVersionId,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	NextAttemptAt   time.Time      `json:"-"`
	IdempotencyKey  string         `json:"-"`
	RequestSHA256   string         `json:"-"`
	Credential      []byte         `json:"-"`
	LeaseID         string         `json:"-"`
	LeaseExpiresAt  *time.Time     `json:"-"`
}

func (Operation) TableName() string { return "function_warehouse_syncs" }

type Store interface {
	Create(context.Context, Operation) (Operation, error)
	Get(context.Context, string) (Operation, error)
	ListJob(context.Context, string, string, string) ([]Operation, error)
	Claim(context.Context, string, time.Time, time.Time) (Operation, error)
	Save(context.Context, Operation, string) error
	Renew(context.Context, string, string, time.Time) error
	Resume(context.Context, string, Actor, []byte) (Operation, error)
	Cancel(context.Context, string, Actor) (Operation, error)
}

// Prepare rechecks current owner/membership, waits for a terminal training job,
// and reuses immutable platform model snapshots. It never trusts caller paths
// as absolute object-storage locations.
type Source interface {
	Prepare(context.Context, Operation) ([]File, SourceIdentity, error)
	Open(context.Context, File) (io.ReadCloser, error)
}

type Upstream interface {
	GetWarehouse(context.Context, string, string) (fw.Warehouse, error)
	ListModelTypes(context.Context, string, string) ([]fw.ModelType, error)
	Upload(context.Context, string, string, string, int64, string, func(context.Context) (io.ReadCloser, error)) (fw.UploadedFile, error)
	CreateVersion(context.Context, string, fw.CreateVersionRequest) (fw.Version, error)
	VerifyVersion(context.Context, string, string, string, fw.CreateVersionRequest) (fw.Version, error)
}
