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
	TrainingEngine     TrainingEngine          `json:"trainingEngine,omitempty"`
	RayVersion         string                  `json:"rayVersion,omitempty"`
	Managed            ManagedTrainingPolicy   `json:"managed,omitempty"`
	DataMode           DataMode                `json:"dataMode,omitempty"`
	ParentJobID        string                  `json:"parentJobId,omitempty"`
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
	ClusterAttempt       int              `json:"clusterAttempt"`
	WorkerRestartCount   int              `json:"workerRestartCount"`
	ResumeCheckpointID   string           `json:"resumeCheckpointId,omitempty"`
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
	// ExpectedClusterAttempt and the expected RayJob identity are copied from
	// the row loaded by the reconciler. They form the compare-and-swap fence
	// that prevents an old attempt from overwriting a newer recovery attempt.
	ExpectedClusterAttempt int
	ExpectedRayJobName     string
	ExpectedRayJobUID      string
	// StartedAt and FinishedAt come from the workload itself, not from the
	// control plane's clock at poll time. Both stay nil until the workload
	// publishes them, so "not started" is never rendered as an epoch date.
	StartedAt  *time.Time
	FinishedAt *time.Time
}

const (
	ManagedRecoveryFailureClassMaxBytes   = 128
	ManagedRecoveryFailureMessageMaxBytes = 4096
)

// NormalizeManagedInfrastructureFailureClass is the complete outer-recovery
// allowlist. It intentionally classifies only pod/cluster loss, eviction,
// deletion and unavailability signals; user exits, code errors, OOM and NaN
// never become recoverable because of message keyword matching.
func NormalizeManagedInfrastructureFailureClass(reason string) (string, bool) {
	normalized := strings.ToUpper(strings.TrimSpace(reason))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "DRIVER_POD_LOST", "DRIVERPODLOST":
		return "DRIVER_POD_LOST", true
	case "DRIVER_POD_EVICTED", "DRIVERPODEVICTED":
		return "DRIVER_POD_EVICTED", true
	case "DRIVER_POD_DELETED", "DRIVERPODDELETED":
		return "DRIVER_POD_DELETED", true
	case "DRIVER_POD_NOT_FOUND", "DRIVERPODNOTFOUND":
		return "DRIVER_POD_NOT_FOUND", true
	case "HEAD_POD_LOST", "HEADPODLOST":
		return "HEAD_POD_LOST", true
	case "HEAD_POD_EVICTED", "HEADPODEVICTED":
		return "HEAD_POD_EVICTED", true
	case "HEAD_POD_DELETED", "HEADPODDELETED":
		return "HEAD_POD_DELETED", true
	case "HEAD_POD_NOT_FOUND", "HEADPODNOTFOUND":
		return "HEAD_POD_NOT_FOUND", true
	case "RAY_CLUSTER_FAILED", "RAYCLUSTERFAILED":
		return "RAY_CLUSTER_FAILED", true
	case "RAY_CLUSTER_UNAVAILABLE", "RAYCLUSTERUNAVAILABLE":
		return "RAY_CLUSTER_UNAVAILABLE", true
	case "RAY_CLUSTER_DELETED", "RAYCLUSTERDELETED":
		return "RAY_CLUSTER_DELETED", true
	case "WHOLE_CLUSTER_UNAVAILABLE", "WHOLECLUSTERUNAVAILABLE":
		return "WHOLE_CLUSTER_UNAVAILABLE", true
	default:
		return "", false
	}
}

// ManagedRecoveryRequest is produced only by the reconciler after it has
// classified a failed managed RayJob. ExpectedClusterAttempt is a CAS token:
// a stale backend replica cannot advance a job that another replica recovered.
type ManagedRecoveryRequest struct {
	JobID                  string
	ExpectedClusterAttempt int
	ExpectedRayJobName     string
	ExpectedRayJobUID      string
	FailureClass           string
	FailureMessage         string
}

func (request ManagedRecoveryRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" {
		return fmt.Errorf("managed recovery job ID is required")
	}
	if request.ExpectedClusterAttempt < 1 {
		return fmt.Errorf("managed recovery expected cluster attempt must be positive")
	}
	if strings.TrimSpace(request.ExpectedRayJobName) == "" || strings.TrimSpace(request.ExpectedRayJobUID) == "" {
		return fmt.Errorf("managed recovery expected RayJob name and UID are required")
	}
	failureClass := strings.TrimSpace(request.FailureClass)
	if failureClass == "" || len(failureClass) > ManagedRecoveryFailureClassMaxBytes {
		return fmt.Errorf("managed recovery failure class is required and bounded")
	}
	if _, ok := NormalizeManagedInfrastructureFailureClass(failureClass); !ok {
		return fmt.Errorf("managed recovery failure class is not recoverable")
	}
	if len(request.FailureMessage) > ManagedRecoveryFailureMessageMaxBytes {
		return fmt.Errorf("managed recovery failure message is too large")
	}
	return nil
}

// ManagedRetiringIdentityRequest clears the previous Kubernetes workload
// identity only after that exact UID has reached NotFound. The attempt, name
// and UID form one CAS fence across backend replicas.
type ManagedRetiringIdentityRequest struct {
	JobID                  string
	ExpectedClusterAttempt int
	RayJobName             string
	RayJobUID              string
}

func (request ManagedRetiringIdentityRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" {
		return fmt.Errorf("managed retiring identity job ID is required")
	}
	if request.ExpectedClusterAttempt < 1 {
		return fmt.Errorf("managed retiring identity expected cluster attempt must be positive")
	}
	if strings.TrimSpace(request.RayJobName) == "" || strings.TrimSpace(request.RayJobUID) == "" {
		return fmt.Errorf("managed retiring RayJob name and UID are required")
	}
	return nil
}

// ManagedAttemptReservationRequest reserves the deterministic Kubernetes name
// for one managed cluster attempt before any create-capable client call. An
// empty ExpectedRayJobName is the first reservation; the deterministic name is
// used as ExpectedRayJobName when a later reconciler revalidates it.
type ManagedAttemptReservationRequest struct {
	JobID                  string
	ExpectedClusterAttempt int
	ExpectedState          State
	ExpectedRayJobName     string
	RayJobName             string
	KubernetesNS           string
}

func (request ManagedAttemptReservationRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" {
		return fmt.Errorf("managed attempt reservation job ID is required")
	}
	if request.ExpectedClusterAttempt < 1 {
		return fmt.Errorf("managed attempt reservation expected cluster attempt must be positive")
	}
	if strings.TrimSpace(string(request.ExpectedState)) == "" {
		return fmt.Errorf("managed attempt reservation expected state is required")
	}
	if !managedAttemptReservableState(request.ExpectedState) {
		return fmt.Errorf("managed attempt reservation expected state is not active")
	}
	name := strings.TrimSpace(request.RayJobName)
	if name == "" || len(name) > 63 || !dnsLabel.MatchString(name) {
		return fmt.Errorf("managed attempt reservation RayJob name must be a DNS label")
	}
	if expected := strings.TrimSpace(request.ExpectedRayJobName); expected != "" && expected != name {
		return fmt.Errorf("managed attempt reservation expected name must be empty or deterministic")
	}
	if namespace := strings.TrimSpace(request.KubernetesNS); namespace == "" || len(namespace) > 63 || !dnsLabel.MatchString(namespace) {
		return fmt.Errorf("managed attempt reservation namespace must be a DNS label")
	}
	return nil
}

type ManagedAttemptResourceState string

const (
	ManagedAttemptResourceReserved    ManagedAttemptResourceState = "RESERVED"
	ManagedAttemptResourceCreating    ManagedAttemptResourceState = "CREATING"
	ManagedAttemptResourceActivating  ManagedAttemptResourceState = "ACTIVATING"
	ManagedAttemptResourceActive      ManagedAttemptResourceState = "ACTIVE"
	ManagedAttemptResourceRetiring    ManagedAttemptResourceState = "RETIRING"
	ManagedAttemptResourceCleaned     ManagedAttemptResourceState = "CLEANED"
	ManagedAttemptResourceQuarantined ManagedAttemptResourceState = "QUARANTINED"
)

type ManagedAttemptResource struct {
	JobID            string
	ClusterAttempt   int
	KubernetesNS     string
	RayJobName       string
	RayJobUID        string
	State            ManagedAttemptResourceState
	LeaseOwner       string
	LeaseVersion     int64
	ResourceFence    int64
	LeaseExpiresAt   *time.Time
	CleanupFailures  int
	CleanupLastError string
	NextCheckAt      time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ManagedAttemptCreationLeaseRequest struct {
	JobID                  string
	ExpectedClusterAttempt int
	ExpectedState          State
	RayJobName             string
	LeaseOwner             string
	LeaseDuration          time.Duration
}

func (request ManagedAttemptCreationLeaseRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" || request.ExpectedClusterAttempt < 1 {
		return fmt.Errorf("managed attempt creation job and attempt are required")
	}
	if !managedAttemptReservableState(request.ExpectedState) {
		return fmt.Errorf("managed attempt creation expected state is not active")
	}
	if name := strings.TrimSpace(request.RayJobName); name == "" || len(name) > 63 || !dnsLabel.MatchString(name) {
		return fmt.Errorf("managed attempt creation RayJob name must be a DNS label")
	}
	if owner := strings.TrimSpace(request.LeaseOwner); owner == "" || len(owner) > 128 {
		return fmt.Errorf("managed attempt creation lease owner is required and bounded")
	}
	if request.LeaseDuration < time.Second || request.LeaseDuration > 5*time.Minute {
		return fmt.Errorf("managed attempt creation lease duration must be between one second and five minutes")
	}
	return nil
}

type ManagedAttemptCleanupRequest struct {
	JobID          string
	ClusterAttempt int
	RayJobName     string
	RayJobUID      string
}

func (request ManagedAttemptCleanupRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" || request.ClusterAttempt < 1 {
		return fmt.Errorf("managed attempt cleanup job and attempt are required")
	}
	if strings.TrimSpace(request.RayJobName) == "" {
		return fmt.Errorf("managed attempt cleanup RayJob name is required")
	}
	return nil
}

// ManagedAttemptAdoptionRequest binds the exact UID returned by Kubernetes to
// a previously reserved attempt name before its status is interpreted.
type ManagedAttemptAdoptionRequest struct {
	JobID                  string
	ExpectedClusterAttempt int
	ExpectedState          State
	RayJobName             string
	RayJobUID              string
	KubernetesNS           string
	ResourceVersion        string
	LeaseOwner             string
	LeaseVersion           int64
	ResourceFence          int64
}

func (request ManagedAttemptAdoptionRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" {
		return fmt.Errorf("managed attempt adoption job ID is required")
	}
	if request.ExpectedClusterAttempt < 1 {
		return fmt.Errorf("managed attempt adoption expected cluster attempt must be positive")
	}
	if strings.TrimSpace(string(request.ExpectedState)) == "" {
		return fmt.Errorf("managed attempt adoption expected state is required")
	}
	if !managedAttemptReservableState(request.ExpectedState) {
		return fmt.Errorf("managed attempt adoption expected state is not active")
	}
	if strings.TrimSpace(request.RayJobName) == "" || strings.TrimSpace(request.RayJobUID) == "" {
		return fmt.Errorf("managed attempt adoption RayJob name and UID are required")
	}
	if strings.TrimSpace(request.KubernetesNS) == "" {
		return fmt.Errorf("managed attempt adoption Kubernetes namespace is required")
	}
	if owner := strings.TrimSpace(request.LeaseOwner); owner == "" || len(owner) > 128 || request.LeaseVersion < 1 {
		return fmt.Errorf("managed attempt adoption lease owner and version are required")
	}
	if request.ResourceFence < 1 {
		return fmt.Errorf("managed attempt adoption resource fence is required")
	}
	return nil
}

// ManagedAttemptActivationRequest authorizes or confirms exposing exactly one
// adopted RayJob to Kueue. The fence is bound during adoption and cannot be
// inferred from a later Kubernetes observation.
type ManagedAttemptActivationRequest struct {
	JobID                  string
	ExpectedClusterAttempt int
	RayJobName             string
	RayJobUID              string
	ResourceFence          int64
}

func (request ManagedAttemptActivationRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" || request.ExpectedClusterAttempt < 1 {
		return fmt.Errorf("managed attempt activation job and attempt are required")
	}
	if strings.TrimSpace(request.RayJobName) == "" || strings.TrimSpace(request.RayJobUID) == "" || request.ResourceFence < 1 {
		return fmt.Errorf("managed attempt activation identity and fence are required")
	}
	return nil
}

type ManagedAttemptCleanupFailureRequest struct {
	JobID          string
	ClusterAttempt int
	RayJobName     string
	RayJobUID      string
	Message        string
	Permanent      bool
	ObservedAt     time.Time
}

func (request ManagedAttemptCleanupFailureRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" || request.ClusterAttempt < 1 || strings.TrimSpace(request.RayJobName) == "" {
		return fmt.Errorf("managed attempt cleanup failure identity is required")
	}
	if strings.TrimSpace(request.Message) == "" {
		return fmt.Errorf("managed attempt cleanup failure message is required")
	}
	return nil
}

type ManagedAttemptRetireRequest struct {
	JobID          string
	ClusterAttempt int
	KubernetesNS   string
	RayJobName     string
	RayJobUID      string
}

func (request ManagedAttemptRetireRequest) Validate() error {
	if strings.TrimSpace(request.JobID) == "" || request.ClusterAttempt < 1 {
		return fmt.Errorf("managed attempt retirement job and attempt are required")
	}
	if namespace := strings.TrimSpace(request.KubernetesNS); namespace == "" || len(namespace) > 63 || !dnsLabel.MatchString(namespace) {
		return fmt.Errorf("managed attempt retirement namespace must be a DNS label")
	}
	if name := strings.TrimSpace(request.RayJobName); name == "" || len(name) > 63 || !dnsLabel.MatchString(name) {
		return fmt.Errorf("managed attempt retirement RayJob name must be a DNS label")
	}
	return nil
}

func managedAttemptReservableState(state State) bool {
	switch state {
	case StateSubmitted, StateValidating, StateQueued, StateAdmitted, StateProvisioning, StateRunning, StateRecovering, StateUnknown:
		return true
	default:
		return false
	}
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var digestImage = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-fA-F]{64}$`)
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var imagePathComponent = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
var imageRegistryComponent = regexp.MustCompile(`^[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*(?::[0-9]+)?$`)
var gitCommit = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
var snapshotID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var jobID = regexp.MustCompile(`^job-[0-9a-f]{24}$`)

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
	if s.TrainingEngine.Resolved() == TrainingEngineRayTrain {
		if err := validateManagedEntrypoint(s.Entrypoint); err != nil {
			return err
		}
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
	if err := s.validateTrainingRuntime(); err != nil {
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

func (s JobSpec) validateTrainingRuntime() error {
	switch s.RayVersion {
	case "", RayVersionLegacy, RayVersionProduction, RayVersionCanary:
	default:
		return fmt.Errorf("unsupported Ray version %q", s.RayVersion)
	}

	engine := s.TrainingEngine.Resolved()
	switch engine {
	case TrainingEngineRayDDP:
		if s.Managed != (ManagedTrainingPolicy{}) {
			return fmt.Errorf("managed policy requires ray-train")
		}
	case TrainingEngineRayTrain:
		if s.RayVersion != RayVersionProduction && s.RayVersion != RayVersionCanary {
			return fmt.Errorf("ray-train requires Ray 2.56.1 or Ray 2.58.0")
		}
		if err := s.Managed.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported training engine %q", s.TrainingEngine)
	}

	dataMode := s.DataMode
	if strings.TrimSpace(string(dataMode)) == "" {
		dataMode = ""
	}
	switch dataMode {
	case "", DataModeMount:
	case DataModeCache:
		if s.Cache.Mode != CacheModeRuntime || s.Cache.Preload != CachePreloadInput {
			return fmt.Errorf("cache data mode requires runtime cache with preload=input")
		}
	case DataModeRayData:
		if engine != TrainingEngineRayTrain {
			return fmt.Errorf("ray-data requires ray-train")
		}
	default:
		return fmt.Errorf("unsupported data mode %q", s.DataMode)
	}

	if s.ParentJobID != "" && !jobID.MatchString(s.ParentJobID) {
		return fmt.Errorf("parentJobId must match the platform job ID format")
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
