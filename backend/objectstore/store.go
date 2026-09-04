package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound      = errors.New("object not found")
	ErrAlreadyExists = errors.New("object already exists")
	ErrUnavailable   = errors.New("object store unavailable")
)

type PresignedPut struct {
	URL             string            `json:"url"`
	RequiredHeaders map[string]string `json:"requiredHeaders"`
	ContentLength   int64             `json:"contentLength"`
	ExpiresAt       time.Time         `json:"expiresAt"`
}

type ObjectInfo struct {
	SizeBytes int64
	Metadata  map[string]string
}

type PublicationObjectInfo struct {
	SizeBytes  int64
	SHA256     string
	ETag       string
	ObservedAt time.Time
	Metadata   map[string]string
}

func (info PublicationObjectInfo) Clone() PublicationObjectInfo {
	return PublicationObjectInfo{
		SizeBytes: info.SizeBytes, SHA256: info.SHA256, ETag: info.ETag,
		ObservedAt: info.ObservedAt, Metadata: cloneObjectMetadata(info.Metadata),
	}
}

type PublicationListedObject struct {
	Key        string
	SizeBytes  int64
	SHA256     string
	ETag       string
	ObservedAt time.Time
}

type PublicationObjectPage struct {
	Objects    []PublicationListedObject
	NextCursor string
}

type PublicationSourceBackend interface {
	List(context.Context, string, string, int) (PublicationObjectPage, error)
	Head(context.Context, string) (PublicationObjectInfo, error)
	Get(context.Context, string) (io.ReadCloser, PublicationObjectInfo, error)
}

type PublicationDerivedBackend interface {
	Head(context.Context, string) (PublicationObjectInfo, error)
	PutImmutable(context.Context, string, string, int64, io.Reader) error
	CopyImmutable(context.Context, string, string) error
	Delete(context.Context, string) error
}

func cloneObjectMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	clone := make(map[string]string, len(metadata))
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

// DirectoryPage is deliberately smaller than an object-store list response:
// it contains only direct child directory names and an opaque continuation
// cursor. Bucket names, full prefixes, object keys, and credentials never
// cross the platform API boundary.
type DirectoryPage struct {
	Directories []string `json:"directories"`
	NextCursor  string   `json:"nextCursor,omitempty"`
}

const (
	DataEntryDirectory = "directory"
	DataEntryFile      = "file"
)

// DataEntry is a user-visible child of a governed data-space root. It never
// carries a bucket, object key, credential, or absolute storage prefix.
type DataEntry struct {
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	SizeBytes    int64     `json:"sizeBytes,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

type DataEntryPage struct {
	Entries    []DataEntry `json:"entries"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

// DirectoryLister is separate from Store so source-artifact upload fakes do
// not need TOS list permission. Only the storage catalogue browser uses it.
type DirectoryLister interface {
	ListDirectories(ctx context.Context, rootPrefix, relativePath, cursor string, limit int) (DirectoryPage, error)
}

// PersonalDataDirectoryInitializer creates only the fixed directory markers
// needed by Kubernetes subPath mounts in a new personal data root. It is a
// backend-only capability; no workload receives the object-store credentials.
type PersonalDataDirectoryInitializer interface {
	EnsurePersonalDataDirectories(context.Context, string) error
}

// DataDirectoryInitializer creates the single marker required for an empty
// governed TOS root. It is used for team/public roots before FSX mounts them;
// normal users never receive this capability or a way to choose the root.
type DataDirectoryInitializer interface {
	EnsureDataDirectory(context.Context, string) error
}

// DataSpaceStore is the backend-only object-store capability used by the
// governed data API. Every operation receives a server-derived root and a
// validated relative path; callers never select a raw bucket or object key.
type DataSpaceStore interface {
	ListDataEntries(context.Context, string, string, string, int) (DataEntryPage, error)
	PresignDataPut(context.Context, string, string, string, int64, time.Duration) (PresignedPut, error)
	ReadData(context.Context, string, string) (ArtifactRead, error)
	PutData(context.Context, string, string, string, int64, io.Reader) error
	CreateDataDirectory(context.Context, string, string) error
}

// WorkspaceSnapshotStore copies a server-authorized personal workspace into
// an immutable sibling prefix. Both arguments are derived by the platform;
// this interface must never be exposed as a generic copy API.
type WorkspaceSnapshotStore interface {
	SnapshotWorkspace(context.Context, string, string, string) (int, error)
}

const (
	ArtifactDirectory = "directory"
	ArtifactFile      = "file"
)

// ArtifactEntry is a task-output child visible in the Portal. It deliberately
// excludes the backing bucket and object key: callers navigate only through
// a task-relative path that the API authorizes on every request.
type ArtifactEntry struct {
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	SizeBytes    int64     `json:"sizeBytes,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

type ArtifactPage struct {
	Entries    []ArtifactEntry `json:"entries"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// ArtifactLister limits object-store access to a server-computed task output
// root. It must never be implemented by accepting a bucket or raw object key
// from an HTTP caller.
type ArtifactLister interface {
	ListArtifactEntries(ctx context.Context, taskRoot, relativePath, cursor string, limit int) (ArtifactPage, error)
}

// ArtifactRead is an object stream that has not yet crossed the HTTP boundary.
// Callers must close Content. Only the artifact API may construct a browser
// response from it after checking task ownership and content policy.
type ArtifactRead struct {
	Content     io.ReadCloser
	SizeBytes   int64
	ContentType string
}

type ArtifactReader interface {
	ReadArtifact(ctx context.Context, taskRoot, relativePath string) (ArtifactRead, error)
}

type Store interface {
	PresignPut(context.Context, string, string, int64, time.Duration) (PresignedPut, error)
	Head(context.Context, string) (ObjectInfo, error)
	Put(context.Context, string, string, int64, io.Reader) error
}
