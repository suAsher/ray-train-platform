// Package environmentbuild owns durable, owner-scoped environment publication.
// Kubernetes execution is injected; user builds never execute inside the API.
package environmentbuild

import (
	"context"
	"errors"
	"time"
)

const RegistryHost = "harbor.wellspiking.ai"
const (
	Queued          = "QUEUED"
	Capturing       = "CAPTURING"
	Building        = "BUILDING"
	Validating      = "VALIDATING"
	Pushing         = "PUSHING"
	VerifyingPull   = "VERIFYING_PULL"
	Ready           = "READY"
	AwaitingAuth    = "AWAITING_AUTH"
	Failed          = "FAILED"
	CancelRequested = "CANCEL_REQUESTED"
	Canceled        = "CANCELED"
)

var (
	ErrNotFound           = errors.New("environment operation not found")
	ErrInvalid            = errors.New("invalid environment publication request")
	ErrConflict           = errors.New("environment operation conflicts with its current state")
	ErrAuthorization      = errors.New("Harbor authorization expired or denied; authorize again")
	ErrCredentialCapacity = errors.New("temporary Harbor authorization capacity reached")
	ErrCapacity           = errors.New("temporary environment artifact capacity reached")
	ErrUnavailable        = errors.New("environment publication is unavailable")
)

type Owner struct {
	TenantID string
	UserID   string
}
type Credentials struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

func (Credentials) String() string   { return "[Harbor credentials redacted]" }
func (Credentials) GoString() string { return "[Harbor credentials redacted]" }

type Authorization struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	TenantID  string    `json:"-"`
	OwnerID   string    `json:"-"`
	Username  string    `json:"username"`
	SecretRef string    `json:"-"`
	BuildID   string    `json:"-"`
	Target    string    `json:"-"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

func (Authorization) TableName() string { return "environment_registry_authorizations" }

// CredentialMaterial is registered before Vault.Put, including refs whose
// authorization transaction never completes. This permits crash cleanup without
// Kubernetes Secret list permission.
type CredentialMaterial struct {
	Ref             string `gorm:"primaryKey"`
	AuthorizationID string
	TenantID        string
	OwnerID         string
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

func (CredentialMaterial) TableName() string { return "environment_credential_materials" }

type Build struct {
	ID                    string     `json:"id" gorm:"primaryKey"`
	TenantID              string     `json:"-"`
	OwnerID               string     `json:"-"`
	WorkspaceID           string     `json:"workspaceId"`
	Namespace             string     `json:"-"`
	WorkspaceResourceName string     `json:"-"`
	WorkspaceUID          string     `json:"-"`
	BaseImage             string     `json:"baseImage"`
	WorkspaceImage        string     `json:"workspaceImage"`
	Name                  string     `json:"name"`
	Description           string     `json:"description"`
	Visibility            string     `json:"visibility"`
	Project               string     `json:"project"`
	Repository            string     `json:"repository"`
	Tag                   string     `json:"tag"`
	Status                string     `json:"status"`
	ResumeStatus          string     `json:"-"`
	Message               string     `json:"message"`
	AuthID                string     `json:"-"`
	IdempotencyKey        string     `json:"-"`
	SnapshotJSON          string     `json:"-"`
	ArtifactDigest        string     `json:"-"`
	ImageDigest           string     `json:"imageDigest,omitempty"`
	ImageReference        string     `json:"imageReference,omitempty"`
	ImageID               string     `json:"imageId,omitempty"`
	ChecksJSON            string     `json:"checksJson,omitempty"`
	Attempt               int        `json:"attempt"`
	LeaseOwner            string     `json:"-"`
	LeaseUntil            *time.Time `json:"-"`
	ArtifactExpiresAt     time.Time  `json:"artifactExpiresAt"`
	CleanedAt             *time.Time `json:"-"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

func (Build) TableName() string { return "environment_builds" }
func (b Build) Target() string {
	return RegistryHost + "/" + b.Project + "/" + b.Repository + ":" + b.Tag
}
func (b Build) Terminal() bool {
	return b.Status == Ready || b.Status == Canceled || b.Status == Failed || b.Status == AwaitingAuth
}

type Version struct {
	ID             string    `json:"id" gorm:"primaryKey"`
	BuildID        string    `json:"buildId"`
	TenantID       string    `json:"tenantId"`
	OwnerID        string    `json:"ownerId"`
	Visibility     string    `json:"visibility"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	ImageID        string    `json:"imageId"`
	ImageReference string    `json:"imageReference"`
	BaseImage      string    `json:"baseImage"`
	ChecksJSON     string    `json:"checksJson"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (Version) TableName() string { return "environment_versions" }

type Workspace struct{ ID, TenantID, OwnerID, Namespace, ResourceName, State string }
type WorkspaceSnapshot struct{ UID, Image string }
type StepResult struct {
	Done           bool
	SnapshotJSON   string
	ArtifactDigest string
	ImageDigest    string
	ChecksJSON     string
	Message        string
}

// Step must be idempotent using Build.ID and phase. It observes/creates a bounded
// Job and returns promptly. Only Pushing receives credentials. Cleanup must
// confirm execution has stopped before returning nil and never deletes registry
// images; retainArtifact keeps only the operation's OCI volume for retry.
type Runner interface {
	InspectWorkspace(context.Context, Workspace) (WorkspaceSnapshot, error)
	Step(context.Context, Build, *Credentials) (StepResult, error)
	Cleanup(context.Context, Build, bool) error
}

// Vault stores only application-encrypted bytes in an isolated credential store.
type Vault interface {
	Put(context.Context, string, []byte, time.Time) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}
type Project struct {
	Name      string `json:"name"`
	ProjectID int64  `json:"projectId"`
	CanPush   bool   `json:"canPush"`
}
type Registry interface {
	Authenticate(context.Context, Credentials) error
	Projects(context.Context, Credentials, int) ([]Project, error)
	CheckPush(context.Context, Credentials, string) error
}
type Store interface {
	ReserveEnvironmentCredentialMaterial(context.Context, CredentialMaterial) error
	EnvironmentCredentialMaterials(context.Context, string) ([]CredentialMaterial, error)
	ExpiredEnvironmentCredentialMaterials(context.Context, time.Time) ([]CredentialMaterial, error)
	DeleteEnvironmentCredentialMaterial(context.Context, string) error
	EnvironmentWorkspace(context.Context, Owner, string) (Workspace, error)
	SaveEnvironmentAuthorization(context.Context, Authorization) error
	EnvironmentAuthorization(context.Context, Owner, string) (Authorization, error)
	DeleteEnvironmentAuthorization(context.Context, string) error
	ExpiredEnvironmentAuthorizations(context.Context, time.Time) ([]Authorization, error)
	CreateEnvironmentBuild(context.Context, Build) (Build, error)
	EnvironmentBuild(context.Context, Owner, string) (Build, error)
	ListEnvironmentBuilds(context.Context, Owner) ([]Build, error)
	ListEnvironmentVersions(context.Context, Owner) ([]Version, error)
	ClaimEnvironmentBuild(context.Context, string, time.Time, time.Duration, int, int) (*Build, error)
	SaveEnvironmentBuild(context.Context, Build, string) error
	RetryEnvironmentBuild(context.Context, Owner, string, string, time.Time) (Build, error)
	CancelEnvironmentBuild(context.Context, Owner, string) (Build, error)
	FinalizeEnvironmentBuild(context.Context, Build, string) error
}
type Config struct {
	Enabled           bool
	BaseImage         string
	WorkspaceImage    string
	EncryptionKey     []byte
	GlobalConcurrency int
	UserConcurrency   int
	AuthorizationTTL  time.Duration
}
