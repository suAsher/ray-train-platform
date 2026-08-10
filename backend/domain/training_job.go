package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type CodeSource struct {
	Type              string `json:"type"`
	URL               string `json:"url,omitempty"`
	Commit            string `json:"commit,omitempty"`
	URI               string `json:"uri,omitempty"`
	Snapshot          string `json:"snapshot,omitempty"`
	ArtifactID        string `json:"artifactId,omitempty"`
	ArtifactObjectKey string `json:"artifactObjectKey,omitempty"`
	ArtifactSHA256    string `json:"artifactSha256,omitempty"`
}

type SubmissionOrigin string

const (
	SubmissionOriginPortal SubmissionOrigin = "portal"
	SubmissionOriginAPI    SubmissionOrigin = "api"
	SubmissionOriginRayCLI SubmissionOrigin = "ray-cli"
)

func (origin SubmissionOrigin) Validate() error {
	switch origin {
	case SubmissionOriginPortal, SubmissionOriginAPI, SubmissionOriginRayCLI:
		return nil
	default:
		return fmt.Errorf("unsupported submission origin %q", origin)
	}
}

type Entrypoint struct {
	Command []string `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type Resources struct {
	WorkerReplicas  int    `json:"workerReplicas"`
	GPUsPerWorker   int    `json:"gpusPerWorker"`
	CPUPerWorker    int64  `json:"cpuPerWorker"`
	MemoryPerWorker string `json:"memoryPerWorker"`
}

// TenantQuota is the tenant's GPU budget as the platform enforces it.
type TenantQuota struct {
	TenantID     string `json:"tenantId"`
	GPULimit     int    `json:"gpuLimit"`
	GPUUsed      int    `json:"gpuUsed"`
	GPUAvailable int    `json:"gpuAvailable"`
}

type CleanupPolicy struct {
	SuccessTTLSeconds int64 `json:"successTtlSeconds"`
	FailureTTLSeconds int64 `json:"failureTtlSeconds"`
}

type RetryPolicy struct {
	MaxRetries int `json:"maxRetries"`
}

type JobSpec struct {
	Name           string        `json:"name"`
	Image          string        `json:"image"`
	Source         CodeSource    `json:"source"`
	Entrypoint     Entrypoint    `json:"entrypoint"`
	Resources      Resources     `json:"resources"`
	Queue          string        `json:"queue"`
	Priority       string        `json:"priority,omitempty"`
	DatasetURI     string        `json:"datasetUri,omitempty"`
	CheckpointURI  string        `json:"checkpointUri,omitempty"`
	OutputURI      string        `json:"outputUri,omitempty"`
	TimeoutSeconds int64         `json:"timeoutSeconds,omitempty"`
	RetryPolicy    RetryPolicy   `json:"retryPolicy,omitempty"`
	CleanupPolicy  CleanupPolicy `json:"cleanupPolicy,omitempty"`
}

type TrainingJob struct {
	ID                   string           `json:"id"`
	TenantID             string           `json:"tenantId"`
	UserID               string           `json:"userId"`
	SourceArtifactID     string           `json:"sourceArtifactId,omitempty"`
	SubmissionOrigin     SubmissionOrigin `json:"submissionOrigin"`
	ExternalSubmissionID string           `json:"externalSubmissionId,omitempty"`
	Spec                 JobSpec          `json:"spec"`
	DesiredState         DesiredState     `json:"desiredState"`
	ObservedState        State            `json:"observedState"`
	StatusReason         string           `json:"statusReason"`
	StatusMessage        string           `json:"statusMessage"`
	KubernetesNS         string           `json:"kubernetesNamespace"`
	RayJobName           string           `json:"rayJobName"`
	RayJobUID            string           `json:"rayJobUid"`
	RayClusterName       string           `json:"rayClusterName"`
	ResourceVersion      string           `json:"resourceVersion"`
	CreatedAt            time.Time        `json:"createdAt"`
	UpdatedAt            time.Time        `json:"updatedAt"`
	LastObservedAt       *time.Time       `json:"lastObservedAt,omitempty"`
	FinishedAt           *time.Time       `json:"finishedAt,omitempty"`
}

type JobFilter struct {
	TenantID string
	Status   State
	Keyword  string
	Limit    int
	Offset   int
}

type Page[T any] struct {
	Items  []T   `json:"items"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

type ObservedJobState struct {
	ID              string
	State           State
	Reason          string
	Message         string
	KubernetesNS    string
	RayJobName      string
	RayJobUID       string
	RayClusterName  string
	ResourceVersion string
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var digestImage = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-fA-F]{64}$`)
var gitCommit = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
var snapshotID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s JobSpec) Validate() error {
	if s.Name == "" || len(s.Name) > 63 || !dnsLabel.MatchString(s.Name) {
		return fmt.Errorf("name must be a lowercase DNS label with 1-63 characters")
	}
	if err := ValidatePinnedImage(s.Image); err != nil {
		return err
	}
	if err := s.Source.validate(); err != nil {
		return err
	}
	if len(s.Entrypoint.Command) == 0 || strings.TrimSpace(s.Entrypoint.Command[0]) == "" {
		return fmt.Errorf("entrypoint command is required")
	}
	if s.Resources.WorkerReplicas < 1 || s.Resources.WorkerReplicas > 3 {
		return fmt.Errorf("workerReplicas must be between 1 and 3")
	}
	if s.Resources.GPUsPerWorker < 1 || s.Resources.GPUsPerWorker > 8 {
		return fmt.Errorf("gpusPerWorker must be between 1 and 8")
	}
	if s.Resources.WorkerReplicas*s.Resources.GPUsPerWorker > 24 {
		return fmt.Errorf("total GPUs cannot exceed 24 in V1")
	}
	if strings.TrimSpace(s.Queue) == "" {
		return fmt.Errorf("queue is required")
	}
	if s.RetryPolicy.MaxRetries < 0 || s.RetryPolicy.MaxRetries > 3 {
		return fmt.Errorf("maxRetries must be between 0 and 3")
	}
	return nil
}

func ValidatePinnedImage(image string) error {
	if !digestImage.MatchString(image) {
		return fmt.Errorf("image must be pinned by sha256 digest")
	}
	return nil
}

func (s CodeSource) validate() error {
	switch s.Type {
	case "git":
		parsed, err := url.Parse(s.URL)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
			return fmt.Errorf("git source requires an http(s) URL without embedded credentials")
		}
		if !gitCommit.MatchString(s.Commit) {
			return fmt.Errorf("git source requires url and commit")
		}
	case "tos":
		parsed, err := url.Parse(s.URI)
		objectPath := strings.Trim(parsed.Path, "/")
		if err != nil || parsed.Scheme != "tos" || parsed.Host == "" || objectPath == "" || strings.Contains(objectPath, "..") {
			return fmt.Errorf("tos source requires a tos:// URI")
		}
	case "workspace":
		if !snapshotID.MatchString(s.Snapshot) {
			return fmt.Errorf("workspace source requires snapshot")
		}
	case "artifact":
		if strings.TrimSpace(s.ArtifactID) == "" {
			return fmt.Errorf("artifact source requires artifactId")
		}
	default:
		return fmt.Errorf("unsupported source type %q", s.Type)
	}
	return nil
}
