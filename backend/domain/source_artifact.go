package domain

import (
	"fmt"
	"regexp"
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
	ID               string              `json:"id"`
	TenantID         string              `json:"tenantId"`
	UserID           string              `json:"userId"`
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
	ID        string
	TenantID  string
	UserID    string
	SHA256    string
	SizeBytes int64
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
	objectKey, err := SourceArtifactObjectKey(input.TenantID, input.UserID, input.SHA256)
	if err != nil {
		return SourceArtifact{}, err
	}
	return SourceArtifact{
		ID: input.ID, TenantID: input.TenantID, UserID: input.UserID,
		SHA256: input.SHA256, SizeBytes: input.SizeBytes, ObjectKey: objectKey,
		State: SourceArtifactPending, UploadExpiresAt: uploadExpiresAt.UTC(), CreatedAt: now.UTC(),
	}, nil
}

func SourceArtifactObjectKey(tenantID, userID, digest string) (string, error) {
	if !safeObjectKeySegment(tenantID) {
		return "", fmt.Errorf("tenant id is not safe for an object key")
	}
	if !safeObjectKeySegment(userID) {
		return "", fmt.Errorf("user id is not safe for an object key")
	}
	if !artifactSHA256.MatchString(digest) {
		return "", fmt.Errorf("sha256 must be 64 lowercase hexadecimal characters")
	}
	return fmt.Sprintf("tenants/%s/users/%s/sha256/%s.zip", tenantID, userID, digest), nil
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
	wantKey, err := SourceArtifactObjectKey(artifact.TenantID, artifact.UserID, artifact.SHA256)
	if err != nil {
		return err
	}
	if artifact.ObjectKey != wantKey {
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
