// Package modellifecycle implements the shared model catalog and immutable snapshots.
// Authentication and resource authorization are enforced by the API before calling it.
package modellifecycle

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	MaxFileSize int64 = 20 << 30
	PartSize          = 8 << 20
	OwnerBudget int64 = 100 << 30
	MaxPending        = 16
	Pending           = "PENDING"
	Copying           = "COPYING"
	Ready             = "READY"
	Failed            = "FAILED"
)

var (
	ErrNotFound    = errors.New("model resource not found")
	ErrConflict    = errors.New("model resource revision or request conflicts")
	ErrInvalid     = errors.New("invalid model request")
	ErrQuota       = errors.New("model snapshot quota exceeded")
	ErrUnavailable = errors.New("model snapshot storage unavailable")
	ErrNotReady    = errors.New("model snapshot is not ready")
)

type Actor struct {
	ID   string
	Name string
}
type Model struct {
	ID             string    `json:"id" gorm:"primaryKey"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	OwnerID        string    `json:"ownerId"`
	OwnerName      string    `json:"ownerName"`
	TenantID       string    `json:"tenantId"`
	Archived       bool      `json:"archived"`
	Revision       int64     `json:"revision"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	IdempotencyKey string    `json:"-"`
	RequestSHA256  string    `json:"-"`
}

func (Model) TableName() string { return "model_catalog" }

type Part struct {
	Index     int    `json:"index"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type Version struct {
	ID                    string     `json:"id" gorm:"primaryKey"`
	ModelID               string     `json:"modelId"`
	Number                int64      `json:"number"`
	Description           string     `json:"description"`
	CreatorID             string     `json:"creatorId"`
	CreatorName           string     `json:"creatorName"`
	JobID                 string     `json:"jobId"`
	JobName               string     `json:"jobName"`
	RunID                 string     `json:"runId,omitempty"`
	FileName              string     `json:"fileName"`
	CodeSHA256            string     `json:"codeSha256,omitempty"`
	CodeCommit            string     `json:"codeCommit,omitempty"`
	RuntimeImage          string     `json:"runtimeImage,omitempty"`
	DatasetAssociation    string     `json:"datasetAssociation,omitempty"`
	DatasetID             string     `json:"datasetId,omitempty"`
	DatasetVersionID      string     `json:"datasetVersionId,omitempty"`
	DatasetName           string     `json:"datasetName,omitempty"`
	DatasetManifestSHA256 string     `json:"datasetManifestSha256,omitempty"`
	SizeBytes             int64      `json:"sizeBytes"`
	SHA256                string     `json:"sha256"`
	State                 string     `json:"state"`
	Error                 string     `json:"error,omitempty"`
	Revision              int64      `json:"revision"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
	SourceRoot            string     `json:"-"`
	SourceETag            string     `json:"-" gorm:"column:source_etag"`
	RelativePath          string     `json:"-"`
	IdempotencyKey        string     `json:"-"`
	RequestSHA256         string     `json:"-"`
	LeaseID               string     `json:"-"`
	LeaseExpiresAt        *time.Time `json:"-"`
	Parts                 []Part     `json:"-" gorm:"serializer:json;type:jsonb"`
}

func (Version) TableName() string { return "model_versions" }

type Filter struct {
	Q        string
	OwnerID  string
	Archived bool
	Cursor   string
	Limit    int
}
type ModelUpdate struct {
	Name        *string
	Description *string
	Archived    *bool
	Revision    int64
}
type VersionUpdate struct {
	Description string
	Revision    int64
}
type ModelPage struct {
	Items      []Model `json:"items"`
	NextCursor string  `json:"nextCursor,omitempty"`
}
type VersionPage struct {
	Items      []Version `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}
type VersionRequest struct {
	ModelID, Description, CreatorID, CreatorName, JobID, JobName, RunID, FileName string
	SourceRoot, RelativePath, CodeSHA256                                          string
	DatasetID, DatasetVersionID, DatasetName, DatasetManifestSHA256               string
	IdempotencyKey                                                                string
	CodeCommit, RuntimeImage, DatasetAssociation                                  string
}
type Repository interface {
	CreateModel(context.Context, Model) (Model, error)
	GetModel(context.Context, string) (Model, error)
	ListModels(context.Context, Filter) (ModelPage, error)
	UpdateModel(context.Context, string, ModelUpdate, Actor) (Model, error)
	ListVersions(context.Context, string, string, int) (VersionPage, error)
	GetVersion(context.Context, string, string) (Version, error)
	UpdateVersion(context.Context, string, string, VersionUpdate, Actor) (Version, error)
	ReserveVersion(context.Context, Version) (Version, error)
	FindVersionRequest(context.Context, string, string, string) (Version, error)
	ClaimVersion(context.Context, string, time.Time, time.Time) (Version, error)
	RenewVersion(context.Context, string, string, time.Time, time.Time) error
	FinishVersion(context.Context, string, string, string, []Part, string, time.Time) error
}

// Source returns the identity token from the same GET that opened the body.
// The ETag is opaque and is never supplied by API clients.
type Source interface {
	Read(context.Context, string, string) (io.ReadCloser, int64, string, error)
}
type Objects interface {
	Put(context.Context, string, int, string, []byte) error
	Get(context.Context, string, int) (io.ReadCloser, int64, error)
}
