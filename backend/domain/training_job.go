package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
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

type CacheMode string
type CachePreloadMode string

const (
	CacheModeOff     CacheMode = "off"
	CacheModeRuntime CacheMode = "runtime"

	CachePreloadInput CachePreloadMode = "input"
)

type CacheRequest struct {
	Mode    CacheMode        `json:"mode,omitempty"`
	Size    string           `json:"size,omitempty"`
	Preload CachePreloadMode `json:"preload,omitempty"`
}

func (cache CacheRequest) Validate() error {
	size := strings.TrimSpace(cache.Size)
	preload := CachePreloadMode(strings.TrimSpace(string(cache.Preload)))
	switch cache.Mode {
	case "", CacheModeOff:
		if size != "" {
			return fmt.Errorf("off cache cannot specify size")
		}
		if preload != "" {
			return fmt.Errorf("off cache cannot specify preload")
		}
		return nil
	case CacheModeRuntime:
		if size == "" {
			return fmt.Errorf("runtime cache size is required")
		}
		if preload != "" && preload != CachePreloadInput {
			return fmt.Errorf("unsupported cache preload %q", preload)
		}
		quantity, err := resource.ParseQuantity(size)
		if err != nil || quantity.Sign() <= 0 {
			return fmt.Errorf("runtime cache size must be a positive Kubernetes storage quantity")
		}
		return nil
	default:
		return fmt.Errorf("unsupported cache mode %q", cache.Mode)
	}
}

type JobSpec struct {
	Name               string                  `json:"name"`
	Image              string                  `json:"image"`
	Source             CodeSource              `json:"source"`
	Entrypoint         Entrypoint              `json:"entrypoint"`
	Execution          ExecutionProfile        `json:"execution,omitempty"`
	Resources          Resources               `json:"resources"`
	Queue              string                  `json:"queue"`
	Priority           string                  `json:"priority,omitempty"`
	DatasetURI         string                  `json:"datasetUri,omitempty"`
	CheckpointURI      string                  `json:"checkpointUri,omitempty"`
	OutputURI          string                  `json:"outputUri,omitempty"`
	DatasetStorage     StorageSelection        `json:"datasetStorage,omitempty"`
	CheckpointStorage  StorageSelection        `json:"checkpointStorage,omitempty"`
	OutputStorage      StorageSelection        `json:"outputStorage,omitempty"`
	Input              DataLocation            `json:"input,omitempty"`
	Checkpoint         DataLocation            `json:"checkpoint,omitempty"`
	Output             DataLocation            `json:"output,omitempty"`
	ResolvedStorage    ResolvedStorageMounts   `json:"resolvedStorage,omitempty"`
	ResolvedDataMounts ResolvedDataSpaceMounts `json:"resolvedDataMounts,omitempty"`
	ResolvedDataRoots  ResolvedDataSpaceRoots  `json:"resolvedDataRoots,omitempty"`
	TimeoutSeconds     int64                   `json:"timeoutSeconds,omitempty"`
	RetryPolicy        RetryPolicy             `json:"retryPolicy,omitempty"`
	CleanupPolicy      CleanupPolicy           `json:"cleanupPolicy,omitempty"`
	Cache              CacheRequest            `json:"cache,omitzero"`
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
	// StartedAt is when the workload began running, which is later than
	// CreatedAt by however long the job waited for a GPU admission.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type JobFilter struct {
	TenantID string
	// AllTenants is reserved for platform-wide administrative views. API
	// handlers must only set it after authorizing a SuperAdmin principal.
	AllTenants bool
	Status     State
	Keyword    string
	Limit      int
	Offset     int
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
	// StartedAt and FinishedAt come from the workload itself, not from the
	// control plane's clock at poll time. Both stay nil until the workload
	// publishes them, so "not started" is never rendered as an epoch date.
	StartedAt  *time.Time
	FinishedAt *time.Time
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var digestImage = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-fA-F]{64}$`)
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var imagePathComponent = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
var imageRegistryComponent = regexp.MustCompile(`^[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*(?::[0-9]+)?$`)
var gitCommit = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
var snapshotID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s JobSpec) Validate() error {
	if s.Name == "" || len(s.Name) > 63 || !dnsLabel.MatchString(s.Name) {
		return fmt.Errorf("name must be a lowercase DNS label with 1-63 characters")
	}
	if err := ValidateRuntimeImage(s.Image); err != nil {
		return err
	}
	if err := s.Source.validate(); err != nil {
		return err
	}
	if len(s.Entrypoint.Command) == 0 || strings.TrimSpace(s.Entrypoint.Command[0]) == "" {
		return fmt.Errorf("entrypoint command is required")
	}
	limits := CurrentResourceLimits()
	if s.Resources.WorkerReplicas < 1 || s.Resources.WorkerReplicas > limits.MaxWorkerReplicas {
		return fmt.Errorf("workerReplicas must be between 1 and %d", limits.MaxWorkerReplicas)
	}
	if s.Resources.GPUsPerWorker < 1 || s.Resources.GPUsPerWorker > limits.MaxGPUsPerWorker {
		return fmt.Errorf("gpusPerWorker must be between 1 and %d", limits.MaxGPUsPerWorker)
	}
	if s.Resources.WorkerReplicas*s.Resources.GPUsPerWorker > limits.MaxTotalGPUs {
		return fmt.Errorf("total GPUs cannot exceed %d", limits.MaxTotalGPUs)
	}
	if err := s.Execution.Validate(s.Resources); err != nil {
		return err
	}
	if strings.TrimSpace(s.Queue) == "" {
		return fmt.Errorf("queue is required")
	}
	if err := s.Cache.Validate(); err != nil {
		return err
	}
	if err := validateDataLocations(
		dataLocation{field: "datasetUri", value: s.DatasetURI},
		dataLocation{field: "checkpointUri", value: s.CheckpointURI},
		dataLocation{field: "outputUri", value: s.OutputURI},
	); err != nil {
		return err
	}
	if err := validateStorageSelections(s); err != nil {
		return err
	}
	if err := validateLogicalDataLocations(s); err != nil {
		return err
	}
	if s.Cache.Preload == CachePreloadInput {
		if s.Input.Space == "" {
			return fmt.Errorf("automatic cache preload requires a governed input data space")
		}
		if strings.TrimSpace(s.Input.RelativePath) == "" {
			return fmt.Errorf("automatic cache preload requires a non-empty input path")
		}
	}
	if err := s.ResolvedStorage.Validate(); err != nil {
		return err
	}
	if err := s.ResolvedDataMounts.Validate(); err != nil {
		return err
	}
	if err := s.ResolvedDataRoots.Validate(); err != nil {
		return err
	}
	if s.RetryPolicy.MaxRetries < 0 || s.RetryPolicy.MaxRetries > 3 {
		return fmt.Errorf("maxRetries must be between 0 and 3")
	}
	return nil
}

// NormalizeImageReference trims surrounding whitespace. Digests are usually
// pasted from a build log, and a trailing newline would otherwise turn into a
// confusing "not in the allowlist" rejection.
func NormalizeImageReference(image string) string {
	return strings.TrimSpace(image)
}

func ValidatePinnedImage(image string) error {
	if !digestImage.MatchString(image) {
		return fmt.Errorf("image must be pinned by sha256 digest")
	}
	return nil
}

// ValidateRuntimeImage accepts the two administrator-controlled forms users
// can select from the image catalogue: an immutable digest or an explicit
// tag. A bare repository is rejected because Kubernetes would silently apply
// the mutable "latest" tag.
func ValidateRuntimeImage(image string) error {
	if digestImage.MatchString(image) {
		return nil
	}
	if !validTaggedImage(image) {
		return fmt.Errorf("image must include an explicit tag or sha256 digest")
	}
	return nil
}

func validTaggedImage(image string) bool {
	if image == "" || strings.ContainsAny(image, "@ \t\r\n") || strings.Contains(image, "//") {
		return false
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon <= lastSlash || lastColon == len(image)-1 {
		return false
	}
	if strings.Count(image[lastSlash+1:], ":") != 1 || !imageTag.MatchString(image[lastColon+1:]) {
		return false
	}
	repository := image[:lastColon]
	parts := strings.Split(repository, "/")
	for index, part := range parts {
		if part == "" {
			return false
		}
		if index == 0 && len(parts) > 1 && (strings.ContainsAny(part, ".:") || part == "localhost") {
			if !imageRegistryComponent.MatchString(part) {
				return false
			}
			continue
		}
		if !imagePathComponent.MatchString(part) {
			return false
		}
	}
	return true
}

func IsPinnedImage(image string) bool {
	return digestImage.MatchString(image)
}

// RuntimeImagePullPolicy prevents a mutable tag from being satisfied by a
// stale node cache. Digest images remain cacheable because their content is
// immutable by definition.
func RuntimeImagePullPolicy(image string) string {
	if IsPinnedImage(image) {
		return "IfNotPresent"
	}
	return "Always"
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
	case "workspace-archive":
		if strings.TrimSpace(s.ArtifactID) == "" {
			return fmt.Errorf("workspace archive source requires artifactId")
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
