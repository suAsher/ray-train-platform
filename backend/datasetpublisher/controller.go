package datasetpublisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

var (
	ErrInvalidPublicationController        = errors.New("invalid dataset publication controller")
	ErrInvalidPublicationControllerRequest = errors.New("invalid dataset publication request")
	ErrPublicationControllerUnavailable    = errors.New("dataset publication controller unavailable")
	ErrPublicationJobUnavailable           = errors.New("dataset publication job unavailable")
	ErrPublicationJobFailed                = errors.New("dataset publication job failed")
	publicationIRSARoleTRNPattern          = regexp.MustCompile(`^trn:iam::[0-9]+:role/[A-Za-z0-9+=,.@_/-]+$`)
)

const maxPublicationJobDuration = 30 * 24 * time.Hour

type PublicationRunRepository interface {
	EnsureDatasetPublicationRun(context.Context, string, bool, domain.DatasetPublicationRun) (domain.DatasetPublicationRun, error)
	ClaimDatasetPublicationRun(context.Context, string, bool, string, string, string, time.Time) (domain.DatasetPublicationRun, bool, error)
	CompareAndSwapDatasetPublicationRun(context.Context, string, bool, domain.DatasetVersionState, domain.DatasetPublicationRun, time.Time) (domain.DatasetPublicationRun, bool, error)
	FinalizeDatasetPublicationRun(context.Context, string, bool, domain.DatasetVersionState, domain.DatasetPublicationRun, domain.DatasetPublicationReceipt, string, time.Time) (domain.DatasetPublicationRun, bool, error)
}

type PublicationJobClient interface {
	EnsurePublicationJob(context.Context, PublicationJobSpec) (PublicationJobStatus, error)
}

type PublicationJobPhase string

const (
	PublicationJobPending     PublicationJobPhase = "PENDING"
	PublicationJobStabilizing PublicationJobPhase = "STABILIZING"
	PublicationJobValidating  PublicationJobPhase = "VALIDATING"
	PublicationJobPacking     PublicationJobPhase = "PACKING"
	PublicationJobSucceeded   PublicationJobPhase = "SUCCEEDED"
	PublicationJobFailed      PublicationJobPhase = "FAILED"
)

type PublicationProgress struct {
	TotalPartitions      int64
	CompletedPartitions  int64
	FailedPartitions     int64
	SourceObjectCount    int64
	ProcessedObjectCount int64
	FailedObjectCount    int64
}

type PublicationJobStatus struct {
	Phase    PublicationJobPhase
	Progress PublicationProgress
	Receipt  *domain.DatasetPublicationReceipt
}

type ReconcileRequest struct {
	TenantID         string
	SuperAdmin       bool
	RunID            string
	DatasetID        string
	DatasetVersionID string
	Version          string
	SchemaVersion    string
	SourceRoot       string
	SourceIndex      string
}

type ControllerOptions struct {
	Namespace             string
	Image                 string
	SourceBucket          string
	TargetBucket          string
	TOSEndpoint           string
	TOSRegion             string
	ImagePullPolicy       string
	ServiceAccountName    string
	IRSARoleTRN           string
	QueueName             string
	PriorityClassName     string
	WorkingDirectory      string
	InternalPrefix        string
	NodeSelector          map[string]string
	PreferredNodeSelector map[string]string
	Tolerations           []PublicationToleration
	CPURequest            string
	CPULimit              string
	MemoryRequest         string
	MemoryLimit           string
	ClientMaxAttempts     int
	JobBackoffLimit       int
	JobActiveDeadline     time.Duration
	JobTTLAfterFinished   time.Duration
	InitialRetryBackoff   time.Duration
	MaximumRetryBackoff   time.Duration
	Now                   func() time.Time
	Wait                  func(context.Context, time.Duration) error
}

type PublicationToleration struct {
	Key        string
	Operator   string
	Value      string
	Effect     string
	Seconds    int64
	HasSeconds bool
}

// PublicationJobResources deliberately has no GPU field. The only resource
// keys it can render are CPU and memory, so callers cannot accidentally turn a
// publication Job into a GPU workload.
type PublicationJobResources struct {
	cpuRequest    string
	cpuLimit      string
	memoryRequest string
	memoryLimit   string
}

func (resources PublicationJobResources) Requests() map[string]string {
	return map[string]string{"cpu": resources.cpuRequest, "memory": resources.memoryRequest}
}

func (resources PublicationJobResources) Limits() map[string]string {
	return map[string]string{"cpu": resources.cpuLimit, "memory": resources.memoryLimit}
}

// PublicationJobSpec is an immutable value object: all stored fields are
// scalars or scalar-only value objects, and collection views are fresh copies.
// A Kubernetes adapter may translate it, but this package has no Kubernetes
// type dependency.
type PublicationJobSpec struct {
	namespace             string
	name                  string
	runID                 string
	datasetID             string
	datasetVersionID      string
	version               string
	schemaVersion         string
	sourceRoot            string
	sourceIndex           string
	image                 string
	sourceBucket          string
	targetBucket          string
	tosEndpoint           string
	tosRegion             string
	imagePullPolicy       string
	serviceAccountName    string
	irsaRoleTRN           string
	queueName             string
	priorityClassName     string
	workingDirectory      string
	internalPrefix        string
	nodeSelector          map[string]string
	preferredNodeSelector map[string]string
	tolerations           []PublicationToleration
	resources             PublicationJobResources
	backoffLimit          int
	activeDeadline        time.Duration
	ttlAfterFinished      time.Duration
}

func (spec PublicationJobSpec) Namespace() string          { return spec.namespace }
func (spec PublicationJobSpec) Name() string               { return spec.name }
func (spec PublicationJobSpec) RunID() string              { return spec.runID }
func (spec PublicationJobSpec) DatasetID() string          { return spec.datasetID }
func (spec PublicationJobSpec) DatasetVersionID() string   { return spec.datasetVersionID }
func (spec PublicationJobSpec) Version() string            { return spec.version }
func (spec PublicationJobSpec) SchemaVersion() string      { return spec.schemaVersion }
func (spec PublicationJobSpec) SourceRoot() string         { return spec.sourceRoot }
func (spec PublicationJobSpec) SourceIndex() string        { return spec.sourceIndex }
func (spec PublicationJobSpec) Image() string              { return spec.image }
func (spec PublicationJobSpec) SourceBucket() string       { return spec.sourceBucket }
func (spec PublicationJobSpec) TargetBucket() string       { return spec.targetBucket }
func (spec PublicationJobSpec) TOSEndpoint() string        { return spec.tosEndpoint }
func (spec PublicationJobSpec) TOSRegion() string          { return spec.tosRegion }
func (spec PublicationJobSpec) ImagePullPolicy() string    { return spec.imagePullPolicy }
func (spec PublicationJobSpec) ServiceAccountName() string { return spec.serviceAccountName }
func (spec PublicationJobSpec) IRSARoleTRN() string        { return spec.irsaRoleTRN }
func (spec PublicationJobSpec) QueueName() string          { return spec.queueName }
func (spec PublicationJobSpec) PriorityClassName() string  { return spec.priorityClassName }
func (spec PublicationJobSpec) WorkingDirectory() string   { return spec.workingDirectory }
func (spec PublicationJobSpec) InternalPrefix() string     { return spec.internalPrefix }
func (spec PublicationJobSpec) NodeSelector() map[string]string {
	return clonePublicationStringMap(spec.nodeSelector)
}
func (spec PublicationJobSpec) PreferredNodeSelector() map[string]string {
	return clonePublicationStringMap(spec.preferredNodeSelector)
}
func (spec PublicationJobSpec) Tolerations() []PublicationToleration {
	return append([]PublicationToleration(nil), spec.tolerations...)
}
func (spec PublicationJobSpec) Resources() PublicationJobResources {
	return spec.resources
}
func (spec PublicationJobSpec) BackoffLimit() int { return spec.backoffLimit }
func (spec PublicationJobSpec) ActiveDeadline() time.Duration {
	return spec.activeDeadline
}
func (spec PublicationJobSpec) TTLAfterFinished() time.Duration {
	return spec.ttlAfterFinished
}

func (spec PublicationJobSpec) Labels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by":                 "ray-train-platform",
		"app.kubernetes.io/name":                       "dataset-publisher",
		"kueue.x-k8s.io/queue-name":                    spec.queueName,
		"platform.wellspiking.ai/publication-job-name": spec.name,
	}
}

type Controller struct {
	repository PublicationRunRepository
	jobs       PublicationJobClient
	options    ControllerOptions
	now        func() time.Time
	wait       func(context.Context, time.Duration) error
}

func NewController(repository PublicationRunRepository, jobs PublicationJobClient, options ControllerOptions) (*Controller, error) {
	if isNilPublicationDependency(repository) || isNilPublicationDependency(jobs) || !validControllerOptions(options) {
		return nil, ErrInvalidPublicationController
	}
	options = cloneControllerOptions(options)
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	wait := options.Wait
	if wait == nil {
		wait = waitForPublicationRetry
	}
	return &Controller{repository: repository, jobs: jobs, options: options, now: now, wait: wait}, nil
}

func PublicationJobName(runID string) (string, error) {
	if !validIdentifier(runID) {
		return "", ErrInvalidPublicationControllerRequest
	}
	digest := sha256.Sum256([]byte(runID))
	return "dataset-publisher-" + hex.EncodeToString(digest[:16]), nil
}

func (controller *Controller) Reconcile(ctx context.Context, request ReconcileRequest) (domain.DatasetPublicationRun, error) {
	if controller == nil || isNilPublicationDependency(controller.repository) || isNilPublicationDependency(controller.jobs) {
		return domain.DatasetPublicationRun{}, ErrInvalidPublicationController
	}
	if err := cleanContextError(ctx, nil); err != nil {
		return domain.DatasetPublicationRun{}, err
	}
	if !request.valid() {
		return domain.DatasetPublicationRun{}, ErrInvalidPublicationControllerRequest
	}
	if publicationRootsOverlap(request.SourceRoot, controller.options.InternalPrefix) {
		return domain.DatasetPublicationRun{}, ErrInvalidPublicationControllerRequest
	}

	initial := domain.DatasetPublicationRun{
		ID: request.RunID, DatasetID: request.DatasetID,
		DatasetVersionID: request.DatasetVersionID, State: domain.DatasetVersionDiscovering,
	}
	current, err := controller.repository.EnsureDatasetPublicationRun(ctx, request.TenantID, request.SuperAdmin, initial)
	if err != nil {
		return domain.DatasetPublicationRun{}, cleanDependencyError(ctx, err)
	}
	if current.State == domain.DatasetVersionReady {
		return current, nil
	}
	if current.State == domain.DatasetVersionFailed {
		return current, ErrPublicationJobFailed
	}
	if current.State == domain.DatasetVersionDiscovering {
		var claimed bool
		current, claimed, err = controller.repository.ClaimDatasetPublicationRun(
			ctx, request.TenantID, request.SuperAdmin,
			request.DatasetID, request.DatasetVersionID, request.RunID,
			controller.now().UTC(),
		)
		if err != nil {
			return domain.DatasetPublicationRun{}, cleanDependencyError(ctx, err)
		}
		if !claimed {
			return current, nil
		}
	}
	if !activePublicationState(current.State) {
		return current, ErrPublicationControllerUnavailable
	}

	spec, err := controller.jobSpec(request)
	if err != nil {
		return current, err
	}
	status, err := controller.ensurePublicationJob(ctx, spec)
	if err != nil {
		if contextErr := cleanContextError(ctx, err); contextErr != nil {
			return current, contextErr
		}
		return current, ErrPublicationJobUnavailable
	}
	return controller.applyJobStatus(ctx, request, current, status)
}

func (controller *Controller) ensurePublicationJob(ctx context.Context, spec PublicationJobSpec) (PublicationJobStatus, error) {
	delay := controller.options.InitialRetryBackoff
	for attempt := 1; attempt <= controller.options.ClientMaxAttempts; attempt++ {
		if err := cleanContextError(ctx, nil); err != nil {
			return PublicationJobStatus{}, err
		}
		status, err := controller.jobs.EnsurePublicationJob(ctx, spec)
		if err == nil && status.Phase == PublicationJobSucceeded && (!status.Progress.complete() || status.Receipt == nil) {
			// Kubernetes may report Job completion before the terminal Pod status
			// (and its receipt) is observable. Retry the read and leave the
			// persisted run active if the control plane never becomes consistent.
			err = ErrPublicationJobUnavailable
		}
		if err == nil {
			return status, nil
		}
		if contextErr := cleanContextError(ctx, err); contextErr != nil {
			return PublicationJobStatus{}, contextErr
		}
		if attempt == controller.options.ClientMaxAttempts {
			return PublicationJobStatus{}, ErrPublicationJobUnavailable
		}
		if err := controller.wait(ctx, delay); err != nil {
			if contextErr := cleanContextError(ctx, err); contextErr != nil {
				return PublicationJobStatus{}, contextErr
			}
			return PublicationJobStatus{}, ErrPublicationJobUnavailable
		}
		delay = nextPublicationRetryDelay(delay, controller.options.MaximumRetryBackoff)
	}
	return PublicationJobStatus{}, ErrPublicationJobUnavailable
}

func (controller *Controller) applyJobStatus(
	ctx context.Context,
	request ReconcileRequest,
	current domain.DatasetPublicationRun,
	status PublicationJobStatus,
) (domain.DatasetPublicationRun, error) {
	switch status.Phase {
	case PublicationJobPending, PublicationJobStabilizing:
		return current, nil
	case PublicationJobValidating:
		return controller.advanceTo(ctx, request, current, domain.DatasetVersionValidating, status.Progress)
	case PublicationJobPacking:
		return controller.advanceTo(ctx, request, current, domain.DatasetVersionPacking, status.Progress)
	case PublicationJobSucceeded:
		if !status.Progress.complete() || !controller.validReceipt(request, status.Receipt) {
			failed, err := controller.markFailed(ctx, request, current, PublicationProgress{})
			if err != nil {
				return failed, err
			}
			return publicationFailureOutcome(failed, ErrPublicationControllerUnavailable)
		}
		current, err := controller.advanceTo(ctx, request, current, domain.DatasetVersionPacking, status.Progress)
		if err != nil {
			return current, err
		}
		if current.State == domain.DatasetVersionReady {
			return current, nil
		}
		if current.State != domain.DatasetVersionPacking {
			return current, ErrPublicationControllerUnavailable
		}
		next := status.Progress.apply(current, domain.DatasetVersionReady)
		updated, swapped, err := controller.repository.FinalizeDatasetPublicationRun(
			ctx, request.TenantID, request.SuperAdmin, current.State, next,
			*status.Receipt, controller.options.InternalPrefix, controller.now().UTC(),
		)
		if err != nil {
			return current, cleanDependencyError(ctx, err)
		}
		if !swapped {
			return publicationFailureOutcome(updated, ErrPublicationControllerUnavailable)
		}
		return updated, nil
	case PublicationJobFailed:
		failed, err := controller.markFailed(ctx, request, current, status.Progress)
		if err != nil {
			return failed, err
		}
		return publicationFailureOutcome(failed, ErrPublicationJobFailed)
	default:
		return current, ErrPublicationControllerUnavailable
	}
}

func (controller *Controller) validReceipt(request ReconcileRequest, receipt *domain.DatasetPublicationReceipt) bool {
	if receipt == nil || receipt.DatasetID != request.DatasetID || receipt.DatasetVersionID != request.DatasetVersionID {
		return false
	}
	return receipt.ValidateWithInternalPrefix(controller.options.InternalPrefix) == nil
}

func (controller *Controller) advanceTo(
	ctx context.Context,
	request ReconcileRequest,
	current domain.DatasetPublicationRun,
	target domain.DatasetVersionState,
	progress PublicationProgress,
) (domain.DatasetPublicationRun, error) {
	for step := 0; step < 3; step++ {
		if current.State == target || publicationStateOrder(current.State) > publicationStateOrder(target) {
			return current, nil
		}
		nextState, ok := nextPublicationState(current.State)
		if !ok {
			if current.State == domain.DatasetVersionFailed {
				return current, ErrPublicationJobFailed
			}
			return current, ErrPublicationControllerUnavailable
		}
		next := progress.apply(current, nextState)
		if err := next.Validate(); err != nil {
			failed, failErr := controller.markFailed(ctx, request, current, PublicationProgress{})
			if failErr != nil {
				return failed, failErr
			}
			return publicationFailureOutcome(failed, ErrPublicationControllerUnavailable)
		}
		updated, swapped, err := controller.repository.CompareAndSwapDatasetPublicationRun(
			ctx, request.TenantID, request.SuperAdmin, current.State, next, controller.now().UTC(),
		)
		if err != nil {
			return current, cleanDependencyError(ctx, err)
		}
		current = updated
		if !swapped && current.State == domain.DatasetVersionFailed {
			return current, ErrPublicationJobFailed
		}
	}
	if current.State != target {
		return current, ErrPublicationControllerUnavailable
	}
	return current, nil
}

func (controller *Controller) markFailed(
	ctx context.Context,
	request ReconcileRequest,
	current domain.DatasetPublicationRun,
	progress PublicationProgress,
) (domain.DatasetPublicationRun, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if current.State == domain.DatasetVersionReady || current.State == domain.DatasetVersionFailed {
			return current, nil
		}
		if !activePublicationState(current.State) {
			return current, ErrPublicationControllerUnavailable
		}
		next := progress.apply(current, domain.DatasetVersionFailed)
		if err := next.Validate(); err != nil {
			next = current
			next.State = domain.DatasetVersionFailed
		}
		updated, swapped, err := controller.repository.CompareAndSwapDatasetPublicationRun(
			ctx, request.TenantID, request.SuperAdmin, current.State, next, controller.now().UTC(),
		)
		if err != nil {
			return current, cleanDependencyError(ctx, err)
		}
		current = updated
		if swapped {
			return current, nil
		}
	}
	return current, ErrPublicationControllerUnavailable
}

func (controller *Controller) jobSpec(request ReconcileRequest) (PublicationJobSpec, error) {
	name, err := PublicationJobName(request.RunID)
	if err != nil {
		return PublicationJobSpec{}, err
	}
	return PublicationJobSpec{
		namespace: controller.options.Namespace,
		name:      name, runID: request.RunID, datasetID: request.DatasetID,
		datasetVersionID: request.DatasetVersionID, version: request.Version,
		schemaVersion: request.SchemaVersion, sourceRoot: request.SourceRoot,
		sourceIndex: request.SourceIndex, image: controller.options.Image,
		sourceBucket: controller.options.SourceBucket, targetBucket: controller.options.TargetBucket,
		tosEndpoint: controller.options.TOSEndpoint, tosRegion: controller.options.TOSRegion,
		imagePullPolicy:    controller.options.ImagePullPolicy,
		serviceAccountName: controller.options.ServiceAccountName,
		irsaRoleTRN:        controller.options.IRSARoleTRN,
		queueName:          controller.options.QueueName, priorityClassName: controller.options.PriorityClassName,
		workingDirectory:      controller.options.WorkingDirectory,
		internalPrefix:        controller.options.InternalPrefix,
		nodeSelector:          clonePublicationStringMap(controller.options.NodeSelector),
		preferredNodeSelector: clonePublicationStringMap(controller.options.PreferredNodeSelector),
		tolerations:           append([]PublicationToleration(nil), controller.options.Tolerations...),
		resources: PublicationJobResources{
			cpuRequest: controller.options.CPURequest, cpuLimit: controller.options.CPULimit,
			memoryRequest: controller.options.MemoryRequest, memoryLimit: controller.options.MemoryLimit,
		},
		backoffLimit:     controller.options.JobBackoffLimit,
		activeDeadline:   controller.options.JobActiveDeadline,
		ttlAfterFinished: controller.options.JobTTLAfterFinished,
	}, nil
}

func (request ReconcileRequest) valid() bool {
	if !validIdentifier(request.RunID) || !validIdentifier(request.DatasetID) || !validIdentifier(request.DatasetVersionID) ||
		!validIdentifier(request.Version) || !validIdentifier(request.SchemaVersion) ||
		!validPublicationRelativePath(request.SourceRoot) || !validPublicationRelativePath(request.SourceIndex) {
		return false
	}
	if request.SuperAdmin && request.TenantID == "" {
		return true
	}
	return validIdentifier(request.TenantID)
}

func (progress PublicationProgress) apply(run domain.DatasetPublicationRun, state domain.DatasetVersionState) domain.DatasetPublicationRun {
	result := run
	result.State = state
	result.TotalPartitions = progress.TotalPartitions
	result.CompletedPartitions = progress.CompletedPartitions
	result.FailedPartitions = progress.FailedPartitions
	result.SourceObjectCount = progress.SourceObjectCount
	result.ProcessedObjectCount = progress.ProcessedObjectCount
	result.FailedObjectCount = progress.FailedObjectCount
	return result
}

func (progress PublicationProgress) complete() bool {
	return progress.TotalPartitions > 0 && progress.CompletedPartitions == progress.TotalPartitions && progress.FailedPartitions == 0 &&
		progress.SourceObjectCount > 0 && progress.ProcessedObjectCount == progress.SourceObjectCount && progress.FailedObjectCount == 0
}

func validControllerOptions(options ControllerOptions) bool {
	if !isPublicationDNSLabel(options.Namespace) ||
		domain.ValidatePinnedImage(options.Image) != nil ||
		!validPublicationBucket(options.SourceBucket) || !validPublicationBucket(options.TargetBucket) ||
		!validPublicationRegion(options.TOSRegion) || !validPublicationEndpoint(options.TOSEndpoint, options.TOSRegion) ||
		!validPublicationImagePullPolicy(options.ImagePullPolicy) ||
		!isPublicationDNSLabel(options.ServiceAccountName) ||
		!validPublicationIRSARoleTRN(options.IRSARoleTRN) ||
		!isPublicationDNSLabel(options.QueueName) ||
		!isPublicationDNSLabel(options.PriorityClassName) ||
		!validPublicationWorkingDirectory(options.WorkingDirectory) ||
		!validPublicationPlacement(options.NodeSelector, options.PreferredNodeSelector, options.Tolerations) {
		return false
	}
	if _, err := domain.NormalizeDatasetInternalPrefix(options.InternalPrefix); err != nil {
		return false
	}
	if !validPublicationResourceValue(options.CPURequest) || !validPublicationResourceValue(options.CPULimit) ||
		!validPublicationResourceValue(options.MemoryRequest) || !validPublicationResourceValue(options.MemoryLimit) {
		return false
	}
	return options.ClientMaxAttempts >= 1 && options.ClientMaxAttempts <= 10 &&
		options.JobBackoffLimit >= 0 && options.JobBackoffLimit <= 10 &&
		validPublicationJobDuration(options.JobActiveDeadline) &&
		validPublicationJobDuration(options.JobTTLAfterFinished) &&
		options.InitialRetryBackoff >= 0 && options.MaximumRetryBackoff >= options.InitialRetryBackoff
}

func validPublicationIRSARoleTRN(value string) bool {
	return value == "" || strings.TrimSpace(value) == value && publicationIRSARoleTRNPattern.MatchString(value)
}

func validPublicationJobDuration(value time.Duration) bool {
	return value >= time.Second && value <= maxPublicationJobDuration && value%time.Second == 0
}

func validPublicationRelativePath(value string) bool {
	normalized, err := domain.NormalizeDatasetInternalPrefix(value)
	return err == nil && normalized == value
}

func validPublicationBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.TrimSpace(value) != value {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			if character == '-' && (index == 0 || index == len(value)-1) {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func validPublicationEndpoint(value, region string) bool {
	if value == "" || len(value) > 253 || strings.TrimSpace(value) != value || value != strings.ToLower(value) || strings.ContainsAny(value, "/\\:@") || !validPublicationRegion(region) {
		return false
	}
	officialSuffix := ""
	switch {
	case strings.HasSuffix(value, ".ivolces.com"):
		officialSuffix = ".ivolces.com"
	case strings.HasSuffix(value, ".volces.com"):
		officialSuffix = ".volces.com"
	default:
		return false
	}
	labels := strings.Split(strings.TrimSuffix(value, officialSuffix), ".")
	regional := "tos-" + region
	regionalS3 := "tos-s3-" + region
	if len(labels) == 1 {
		return labels[0] == regional || labels[0] == regionalS3
	}
	if len(labels) == 2 {
		return validPublicationBucket(labels[0]) && (labels[1] == regional || labels[1] == regionalS3)
	}
	if officialSuffix == ".ivolces.com" && len(labels) == 3 {
		return validPublicationPrivateEndpointLabel(labels[0]) && labels[1] == region && labels[2] == "tos"
	}
	return false
}

func validPublicationPrivateEndpointLabel(value string) bool {
	if !strings.HasPrefix(value, "tos") || !strings.HasSuffix(value, "-private") {
		return false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(value, "tos"), "-private")
	for _, character := range digits {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validPublicationRegion(value string) bool {
	if value == "" || len(value) > 63 || strings.TrimSpace(value) != value || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return strings.Contains(value, "-")
}

func validPublicationImagePullPolicy(value string) bool {
	return value == "Always" || value == "IfNotPresent" || value == "Never"
}

func validPublicationWorkingDirectory(value string) bool {
	return value != "" && len(value) <= domain.DatasetPathMaxBytes && path.IsAbs(value) && path.Clean(value) == value && value != "/" && strings.IndexFunc(value, func(char rune) bool { return char < 32 || char == 127 }) < 0
}

func validPublicationPlacement(nodeSelector, preferred map[string]string, tolerations []PublicationToleration) bool {
	for _, selector := range []map[string]string{nodeSelector, preferred} {
		for key, value := range selector {
			if key == "" || !validPublicationLabelToken(key, 253, true) || !validPublicationLabelToken(value, 63, false) {
				return false
			}
		}
	}
	if len(tolerations) > 64 {
		return false
	}
	for _, toleration := range tolerations {
		if toleration.Key != "" && !validPublicationLabelToken(toleration.Key, 253, true) {
			return false
		}
		if toleration.Operator != "Equal" && toleration.Operator != "Exists" {
			return false
		}
		if toleration.Operator == "Exists" && toleration.Value != "" {
			return false
		}
		if !validPublicationLabelToken(toleration.Value, 63, false) {
			return false
		}
		if toleration.Effect != "" && toleration.Effect != "NoSchedule" && toleration.Effect != "PreferNoSchedule" && toleration.Effect != "NoExecute" {
			return false
		}
		if toleration.HasSeconds && (toleration.Effect != "NoExecute" || toleration.Seconds < 0) {
			return false
		}
	}
	return true
}

func validPublicationLabelToken(value string, maximum int, allowSlash bool) bool {
	if len(value) > maximum || strings.TrimSpace(value) != value || strings.IndexFunc(value, func(char rune) bool { return char < 32 || char == 127 }) >= 0 {
		return false
	}
	if value == "" {
		return true
	}
	slashCount := 0
	for _, char := range value {
		if char == '/' && allowSlash {
			slashCount++
			continue
		}
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return slashCount <= 1
}

func cloneControllerOptions(options ControllerOptions) ControllerOptions {
	result := options
	result.NodeSelector = clonePublicationStringMap(options.NodeSelector)
	result.PreferredNodeSelector = clonePublicationStringMap(options.PreferredNodeSelector)
	result.Tolerations = append([]PublicationToleration(nil), options.Tolerations...)
	return result
}

func clonePublicationStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func validPublicationResourceValue(value string) bool {
	if value == "" || len(value) > 32 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '.' {
			continue
		}
		return false
	}
	return true
}

func isPublicationDNSLabel(value string) bool {
	if value == "" || len(value) > 63 || strings.TrimSpace(value) != value {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			if char == '-' && (index == 0 || index == len(value)-1) {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func activePublicationState(state domain.DatasetVersionState) bool {
	return state == domain.DatasetVersionStabilizing || state == domain.DatasetVersionValidating || state == domain.DatasetVersionPacking
}

func publicationFailureOutcome(run domain.DatasetPublicationRun, failure error) (domain.DatasetPublicationRun, error) {
	if run.State == domain.DatasetVersionReady {
		return run, nil
	}
	if run.State == domain.DatasetVersionFailed {
		return run, failure
	}
	return run, ErrPublicationControllerUnavailable
}

func isNilPublicationDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func publicationStateOrder(state domain.DatasetVersionState) int {
	switch state {
	case domain.DatasetVersionDiscovering:
		return 0
	case domain.DatasetVersionStabilizing:
		return 1
	case domain.DatasetVersionValidating:
		return 2
	case domain.DatasetVersionPacking:
		return 3
	case domain.DatasetVersionReady:
		return 4
	default:
		return -1
	}
}

func nextPublicationState(state domain.DatasetVersionState) (domain.DatasetVersionState, bool) {
	switch state {
	case domain.DatasetVersionStabilizing:
		return domain.DatasetVersionValidating, true
	case domain.DatasetVersionValidating:
		return domain.DatasetVersionPacking, true
	case domain.DatasetVersionPacking:
		return domain.DatasetVersionReady, true
	default:
		return "", false
	}
}

func nextPublicationRetryDelay(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum-current {
		return maximum
	}
	return current * 2
}

func waitForPublicationRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cleanContextError(ctx context.Context, err error) error {
	if ctx == nil {
		return ErrInvalidPublicationControllerRequest
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func cleanDependencyError(ctx context.Context, err error) error {
	if contextErr := cleanContextError(ctx, err); contextErr != nil {
		return contextErr
	}
	return ErrPublicationControllerUnavailable
}
