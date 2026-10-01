// Package storagesync coordinates administrator-managed copies without changing
// the immutable dataset publication pipeline.
package storagesync

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("storage sync not found")
	ErrConflict = errors.New("storage sync conflict")
	ErrLocked = errors.New("storage path is locked by another run")
	ErrForbidden = errors.New("storage sync authorization revoked")
	ErrStaleAttempt = errors.New("stale storage sync attempt")
	ErrPreviewInvalid = errors.New("preview expired or changed; preview again")
	ErrInvalid = errors.New("invalid storage sync request")
)

type Location struct {
	SpaceID string `json:"spaceId"`
	TenantID string `json:"tenantId,omitempty"`
	RelativePath string `json:"relativePath"`
}
type Mapping struct {
	Source Location `json:"source"`
	Destination Location `json:"destination"`
	Layout string `json:"layout"`
}
type Schedule struct {
	Kind string `json:"kind"`
	Timezone string `json:"timezone"`
	EveryHours int `json:"everyHours,omitempty"`
	Time string `json:"time,omitempty"`
	Weekday int `json:"weekday,omitempty"`
}
type Config struct {
	Mode string `json:"mode"`
	ConflictPolicy string `json:"conflictPolicy"`
	Verification string `json:"verification"`
	Mappings []Mapping `json:"mappings"`
	Schedule Schedule `json:"schedule"`
	Concurrency int `json:"concurrency"`
	BandwidthBytesPerSecond int64 `json:"bandwidthBytesPerSecond"`
}

// ResolvedLocation is an internal snapshot. Never marshal it directly to a
// public API; Plan uses logical Config and Run/Preview hide Resolved.
type ResolvedLocation struct {
	Location
	Kind string `json:"kind"`
	StorageID string `json:"storageId"`
	Region string `json:"region,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	NFSServer string `json:"nfsServer,omitempty"`
	NFSRoot string `json:"nfsRoot,omitempty"`
	Revision string `json:"revision"`
}
type ResolvedMapping struct {
	Source ResolvedLocation `json:"source"`
	Destination ResolvedLocation `json:"destination"`
	Layout string `json:"layout"`
}
type Plan struct {
	ID string `json:"id"`
	Name string `json:"name"`
	CreatedBy string `json:"createdBy"`
	Owner string `json:"owner,omitempty"`
	Revision int64 `json:"revision"`
	Enabled bool `json:"enabled"`
	Config Config `json:"config"`
	NextRunAt *time.Time `json:"nextRunAt"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	SkippedSchedules int64 `json:"skippedSchedules"`
	FailureReason string `json:"failureReason,omitempty"`
}
type Progress struct {
	DiscoveredFiles int64 `json:"discoveredFiles"`
	SourceFiles int64 `json:"sourceFiles"`
	SourceBytes int64 `json:"sourceBytes"`
	TransferFiles int64 `json:"transferFiles"`
	TransferBytes int64 `json:"transferBytes"`
	ReusedFiles int64 `json:"reusedFiles"`
	ReusedBytes int64 `json:"reusedBytes"`
	CompletedFiles int64 `json:"completedFiles"`
	CompletedBytes int64 `json:"completedBytes"`
	VerifiedFiles int64 `json:"verifiedFiles"`
	VerifiedBytes int64 `json:"verifiedBytes"`
	FailedFiles int64 `json:"failedFiles"`
	TargetExtraFiles int64 `json:"targetExtraFiles"`
	InFlightBytes int64 `json:"inFlightBytes"`
	NetworkBytes int64 `json:"networkBytes"`
	ScanComplete bool `json:"scanComplete"`
}
type MappingProgress struct {
	MappingIndex int `json:"mappingIndex"`
	Progress Progress `json:"progress"`
}
type FileReference struct {
	Path string `json:"-"`
	Digest string `json:"digest"`
	Count int64 `json:"count"`
}
type BrowseEntry struct {
	Name string `json:"name"`
	RelativePath string `json:"relativePath"`
	Kind string `json:"kind"`
	SizeBytes int64 `json:"sizeBytes"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
}
type FileResult struct {
	MappingIndex int `json:"mappingIndex"`
	RelativePath string `json:"relativePath"`
	State string `json:"state"`
	SizeBytes int64 `json:"sizeBytes"`
	ErrorCode string `json:"errorCode,omitempty"`
}
type FilePage struct {
	Items []FileResult `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
type Preview struct {
	ID string `json:"id"`
	Kind string `json:"kind"`
	PlanID string `json:"planId,omitempty"`
	Actor string `json:"actor"`
	ConfigRevision int64 `json:"configRevision"`
	Config Config `json:"config"`
	Resolved []ResolvedMapping `json:"-"`
	ResolutionDigest string `json:"-"`
	BaselineRef string `json:"-"`
	State string `json:"state"`
	Stage string `json:"stage,omitempty"`
	ManifestDigest string `json:"manifestDigest"`
	SourceFingerprint string `json:"-"`
	TargetFingerprint string `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Progress Progress `json:"progress"`
	MappingProgress []MappingProgress `json:"mappingProgress,omitempty"`
	Files FileReference `json:"files"`
	FailureReason string `json:"failureReason,omitempty"`
	Attempt int `json:"-"`
	Generation int64 `json:"-"`
	Sequence int64 `json:"-"`
	JobUID string `json:"-"`
	WorkerID string `json:"-"`
	ReceiptState string `json:"-"`
	LastReportDigest string `json:"-"`
	RequestsDrained bool `json:"-"`
	Location Location `json:"location"`
	Cursor string `json:"-"`
	Limit int `json:"limit,omitempty"`
	BrowseEntries []BrowseEntry `json:"entries,omitempty"`
	NextCursor string `json:"nextCursor,omitempty"`
}
type Run struct {
	ID string `json:"id"`
	PlanID string `json:"planId"`
	RequestedBy string `json:"requestedBy"`
	AuthorizedBy string `json:"-"`
	IdempotencyKey string `json:"-"`
	ConfigRevision int64 `json:"configRevision"`
	Config Config `json:"config"`
	Resolved []ResolvedMapping `json:"-"`
	ResolutionDigest string `json:"-"`
	BaselineRef string `json:"-"`
	PreviewID string `json:"previewId,omitempty"`
	State string `json:"state"`
	Phase string `json:"phase"`
	Stage string `json:"stage,omitempty"`
	Trigger string `json:"trigger"`
	Attempt int `json:"attempt"`
	Generation int64 `json:"-"`
	Sequence int64 `json:"-"`
	JobUID string `json:"-"`
	WorkerID string `json:"-"`
	ManifestDigest string `json:"manifestDigest"`
	SourceFingerprint string `json:"-"`
	TargetFingerprint string `json:"-"`
	Progress Progress `json:"progress"`
	MappingProgress []MappingProgress `json:"mappingProgress,omitempty"`
	Files FileReference `json:"files"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	StartedAt *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	HeartbeatAt *time.Time `json:"heartbeatAt"`
	ScheduledAt *time.Time `json:"scheduledAt"`
	RecoverableUntil time.Time `json:"recoverableUntil"`
	FailureReason string `json:"failureReason,omitempty"`
	ReceiptState string `json:"-"`
	LastReportDigest string `json:"-"`
	RequestsDrained bool `json:"-"`
	StopVerified bool `json:"-"`
}
type PathLock struct { StorageID, Region, Bucket, Prefix, Mode string }
type StartRequest struct {
	IdempotencyKey string `json:"-"`
	PreviewID string `json:"previewId"`
	ConfigRevision int64 `json:"configRevision"`
	ManifestDigest string `json:"manifestDigest"`
}
type Resolver interface {
	IsAuthorized(context.Context, string) error
	Resolve(context.Context, string, Location) (ResolvedLocation,error)
}
type JobClient interface {
	Ensure(context.Context, WorkSpec) (Observation,error)
	Observe(context.Context, string, int) (Observation,error)
	Stop(context.Context, string, int) error
}
// ReceiptRecoverer replays an existing durable final receipt with a read-only
// helper Job; it must not run a new writer or manufacture termination evidence.
type ReceiptRecoverer interface {
	RecoverReceipt(context.Context,WorkSpec) error
}
type Observation struct {
	Exists bool
	Running bool
	Terminated bool
	RequestsDrained bool // Not sufficient evidence; authenticated receipt is required.
	JobUID string
	FailureReason string
}
type Repository interface {
	Transact(context.Context, func(Tx) error) error
	ListPlans(context.Context) ([]Plan,error)
	GetPlan(context.Context,string) (Plan,error)
	ListRuns(context.Context,string) ([]Run,error)
	GetRun(context.Context,string) (Run,error)
	GetPreview(context.Context,string) (Preview,error)
	ListRunFiles(context.Context,string,string,int) (FilePage,error)
}
type Tx interface {
	GetPlan(string) (Plan,error)
	PutPlan(Plan) error
	ListPlans() ([]Plan,error)
	GetPreview(string) (Preview,error)
	PutPreview(Preview) error
	ListPreviews() ([]Preview,error)
	GetRun(string) (Run,error)
	PutRun(Run) error
	ListRuns(string) ([]Run,error)
	AcquireLocks(string,int,[]PathLock) error
	ReleaseLocks(string) error
	PutFileResults(string,int,int64,[]FileResult) error
}
