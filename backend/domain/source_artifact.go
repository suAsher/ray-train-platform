package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxSourceArtifactSize int64 = 2 * 1024 * 1024 * 1024

type SourceArtifactState string

const (
	SourceArtifactPending SourceArtifactState = "PENDING"
	SourceArtifactReady   SourceArtifactState = "READY"
)

var (
	artifactSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keySegment     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)
)

type SourceArtifact struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	UserID   string `json:"userId"`
	// StorageRoot is the persisted physical personal root used for this
	// immutable archive. It is intentionally distinct from UserID: ownership
	// remains an opaque identity while storage roots may use an approved login
	// name. It is never exposed through the HTTP API.
	StorageRoot      string              `json:"-"`
	SHA256           string              `json:"sha256"`
	SizeBytes        int64               `json:"sizeBytes"`
	ObjectKey        string              `json:"-"`
	State            SourceArtifactState `json:"state"`
	UploadExpiresAt  time.Time           `json:"uploadExpiresAt"`
	CompletedAt      *time.Time          `json:"completedAt,omitempty"`
	LastReferencedAt *time.Time          `json:"lastReferencedAt,omitempty"`
	CreatedAt        time.Time           `json:"createdAt"`
}

type SourceArtifactInput struct {
	ID          string
	TenantID    string
	UserID      string
	StorageRoot string
	SHA256      string
	SizeBytes   int64
}

func NewSourceArtifact(input SourceArtifactInput, uploadExpiresAt, now time.Time) (SourceArtifact, error) {
	if input.ID == "" {
		return SourceArtifact{}, fmt.Errorf("artifact id is required")
	}
	if input.SizeBytes < 1 || input.SizeBytes > MaxSourceArtifactSize {
		return SourceArtifact{}, fmt.Errorf("artifact size must be between 1 and %d bytes", MaxSourceArtifactSize)
	}
	if !uploadExpiresAt.After(now) {
		return SourceArtifact{}, fmt.Errorf("upload expiry must be after creation")
	}
	storageRoot, err := sourceArtifactStorageRoot(input.TenantID, input.UserID, input.StorageRoot)
	if err != nil {
		return SourceArtifact{}, err
	}
	objectKey, err := SourceArtifactObjectKeyForRoot(input.TenantID, storageRoot, input.SHA256)
	if err != nil {
		return SourceArtifact{}, err
	}
	return SourceArtifact{
		ID: input.ID, TenantID: input.TenantID, UserID: input.UserID, StorageRoot: storageRoot,
		SHA256: input.SHA256, SizeBytes: input.SizeBytes, ObjectKey: objectKey,
		State: SourceArtifactPending, UploadExpiresAt: uploadExpiresAt.UTC(), CreatedAt: now.UTC(),
	}, nil
}

func SourceArtifactObjectKey(tenantID, userID, digest string) (string, error) {
	root, err := PersonalDataRootFor(tenantID, userID)
	if err != nil {
		return "", err
	}
	return SourceArtifactObjectKeyForRoot(tenantID, root, digest)
}

// SourceArtifactObjectKeyForRoot derives an archive path below a verified
// personal storage root. Callers obtain that root only from a persisted
// DataMountBinding; it is never accepted from an HTTP request.
func SourceArtifactObjectKeyForRoot(tenantID, storageRoot, digest string) (string, error) {
	if _, err := PersonalDataSpacesForRoot(tenantID, storageRoot); err != nil {
		return "", fmt.Errorf("source artifact storage root: %w", err)
	}
	if !artifactSHA256.MatchString(digest) {
		return "", fmt.Errorf("sha256 must be 64 lowercase hexadecimal characters")
	}
	// Ray SDK packages are retained inside the owner's governed workspace
	// root. The Ray init container reads this file through the owner's PVC;
	// it never receives an object-store key or credential.
	return storageRoot + "workspace/.ray-train-archives/" + digest + ".zip", nil
}

func sourceArtifactStorageRoot(tenantID, userID, storageRoot string) (string, error) {
	if storageRoot == "" {
		return PersonalDataRootFor(tenantID, userID)
	}
	if _, err := PersonalDataSpacesForRoot(tenantID, storageRoot); err != nil {
		return "", fmt.Errorf("source artifact storage root: %w", err)
	}
	return storageRoot, nil
}

// IsSourceArtifactObjectKeyForTenant is a renderer-side structural check. The
// submission service has already loaded the artifact owner-scoped from the
// repository; this extra check prevents a malformed hand-crafted RayJob from
// pointing the materializer outside the tenant's personal archive layout.
func IsSourceArtifactObjectKeyForTenant(tenantID, key, digest string) bool {
	prefix := "ray-train/tenants/" + tenantID + "/users/"
	suffix := "/workspace/.ray-train-archives/" + digest + ".zip"
	if !safeObjectKeySegment(tenantID) || !artifactSHA256.MatchString(digest) || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return false
	}
	storageKey := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
	return safeObjectKeySegment(storageKey)
}

func safeObjectKeySegment(value string) bool {
	return value != "." && value != ".." && keySegment.MatchString(value)
}

func (artifact SourceArtifact) Validate() error {
	if artifact.ID == "" {
		return fmt.Errorf("artifact id is required")
	}
	if artifact.SizeBytes < 1 || artifact.SizeBytes > MaxSourceArtifactSize {
		return fmt.Errorf("artifact size is invalid")
	}
	storageRoot, err := sourceArtifactStorageRoot(artifact.TenantID, artifact.UserID, artifact.StorageRoot)
	if err != nil {
		return err
	}
	wantKey, err := SourceArtifactObjectKeyForRoot(artifact.TenantID, storageRoot, artifact.SHA256)
	if err != nil {
		return err
	}
	if artifact.StorageRoot != storageRoot || artifact.ObjectKey != wantKey {
		return fmt.Errorf("artifact object key is not canonical")
	}
	if artifact.State != SourceArtifactPending && artifact.State != SourceArtifactReady {
		return fmt.Errorf("artifact state is invalid")
	}
	return nil
}

func (artifact SourceArtifact) MarkReady(completedAt time.Time) (SourceArtifact, error) {
	if err := artifact.Validate(); err != nil {
		return SourceArtifact{}, err
	}
	if artifact.State == SourceArtifactReady {
		return artifact, nil
	}
	completed := completedAt.UTC()
	ready := artifact
	ready.State = SourceArtifactReady
	ready.CompletedAt = &completed
	return ready, nil
}
