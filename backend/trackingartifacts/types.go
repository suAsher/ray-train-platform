// Package trackingartifacts stores immutable external-run files. The API must
// authorize the run before every call; this package never infers permission
// from a historical owner or accepts a caller-selected object-store path.
package trackingartifacts

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	PartSizeBytes    int64 = 8 << 20
	MaxFileBytes     int64 = 20 << 30
	OwnerBudgetBytes int64 = 100 << 30
	MaxPending             = 16
	MaxParts               = int(MaxFileBytes / PartSizeBytes)
	OperationTimeout       = 15 * time.Minute
)

var (
	ErrInvalid     = errors.New("invalid artifact request")
	ErrNotFound    = errors.New("artifact not found")
	ErrConflict    = errors.New("artifact state or content conflict")
	ErrQuota       = errors.New("artifact storage quota exceeded")
	ErrUnavailable = errors.New("artifact storage unavailable")
)

type Scope struct{ TenantID, OwnerID, RunID string }
type InitInput struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type Part struct {
	Index     int    `json:"index"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type Artifact struct {
	ID            string    `json:"id"`
	RunID         string    `json:"runId"`
	Name          string    `json:"name"`
	SizeBytes     int64     `json:"sizeBytes"`
	SHA256        string    `json:"sha256"`
	State         string    `json:"state"`
	PartSizeBytes int64     `json:"partSizeBytes"`
	TotalParts    int       `json:"totalParts"`
	UploadedParts []Part    `json:"uploadedParts"`
	ExpiresAt     time.Time `json:"expiresAt"`
	CreatedAt     time.Time `json:"createdAt"`
}
type Record struct {
	Artifact
	Scope           Scope
	IdempotencyHash string
}
type Page struct {
	Items      []Artifact `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// Mutate must serialize by artifact ID and atomically commit only on nil error.
// It deliberately holds the artifact row lock across bounded object IO. Reserve
// alone serializes owner-budget admission; no owner lock is held during IO.
type Repository interface {
	Reserve(context.Context, Record) (Record, error)
	Get(context.Context, Scope, string) (Record, error)
	List(context.Context, Scope, string, int) ([]Record, error)
	Mutate(context.Context, Scope, string, func(Record) (Record, error)) (Record, error)
}

// Objects only accepts generated IDs and fixed part indexes, never paths.
// Put is immutable and idempotent only for identical size and SHA256.
type Objects interface {
	Put(context.Context, string, int, string, []byte) error
	Get(context.Context, string, int) (io.ReadCloser, int64, error)
	Delete(context.Context, string, int) error
}
type Service struct {
	repo    Repository
	objects Objects
}

func New(repo Repository, objects Objects) *Service { return &Service{repo: repo, objects: objects} }
