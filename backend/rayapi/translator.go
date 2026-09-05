package rayapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ray-train-platform-backend/domain"
)

const (
	metadataImage              = "ray-platform.image"
	metadataWorkerReplicas     = "ray-platform.worker-replicas"
	metadataGPUsPerWorker      = "ray-platform.gpus-per-worker"
	metadataCPUPerWorker       = "ray-platform.cpu-per-worker"
	metadataMemoryWorker       = "ray-platform.memory-per-worker"
	metadataQueue              = "ray-platform.queue"
	metadataCacheMode          = "platform.cache.mode"
	metadataCacheSize          = "platform.cache.size"
	metadataCachePreload       = "platform.cache.preload"
	metadataInputSpace         = "platform.data.input-space"
	metadataInputPath          = "platform.data.input-path"
	metadataTrainingEngine     = "platform.training.engine"
	metadataDatasetReference   = "platform.dataset.ref"
	metadataDatasetVersion     = "platform.dataset.version"
	metadataDatasetCachePolicy = "platform.dataset.cache-policy"
	nativeManagedOutputRoot    = "native-ray"
)

var (
	digestPackageName = regexp.MustCompile(`^[0-9a-f]{64}\.zip$`)
	rayPackageName    = regexp.MustCompile(`^_ray_pkg_[0-9a-f]{16}\.zip$`)
	dnsLabel          = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	memoryQuantity    = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`)
	submissionID      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func ParsePackageName(protocol, value string) (PackageName, error) {
	if protocol != "gcs" || strings.ContainsAny(value, `/\\?#`) || (!digestPackageName.MatchString(value) && !rayPackageName.MatchString(value)) {
		return PackageName{}, fmt.Errorf("invalid package reference")
	}
	return PackageName{Name: value}, nil
}

func TranslateSubmitRequest(request JobSubmitRequest) (TranslatedSubmitRequest, error) {
	resources, queue, image, err := parseMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	cache, err := parseCacheMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	input, err := parseInputMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	return translateSubmitRequest(request, resources, queue, image, cache, input)
}

// TranslateSubmitRequestWithDefaults accepts the standard Ray CLI shape. With
// no ray-platform resource metadata it chooses the operator-configured 1-GPU
// environment; providing any ray-platform resource metadata opts into the
// explicit all-fields-required contract so a typo cannot produce a hybrid
// workload. Cache-only metadata remains independent of that resource choice.
func TranslateSubmitRequestWithDefaults(request JobSubmitRequest, defaults SubmissionDefaults) (TranslatedSubmitRequest, error) {
	resources, queue, image, err := parseMetadataOrDefaults(request.Metadata, defaults)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	cache, err := parseCacheMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	input, err := parseInputMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	return translateSubmitRequest(request, resources, queue, image, cache, input)
}

func translateSubmitRequest(request JobSubmitRequest, resources domain.Resources, queue, image string, cache domain.CacheRequest, input domain.DataLocation) (TranslatedSubmitRequest, error) {
	engine, managed, err := parseTrainingMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	datasetRef, cachePolicy, err := parseDatasetMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	dataMode := domain.DataMode("")
	if !datasetRef.IsZero() {
		if engine != domain.TrainingEngineRayTrain {
			return TranslatedSubmitRequest{}, fmt.Errorf("streaming dataset requires ray-train")
		}
		if cache != (domain.CacheRequest{}) || input != (domain.DataLocation{}) {
			return TranslatedSubmitRequest{}, fmt.Errorf("streaming dataset cannot use legacy input or cache metadata")
		}
		dataMode = domain.DataModeStreaming
	}
	if strings.TrimSpace(request.Entrypoint) == "" || len(request.Entrypoint) > 8192 {
		return TranslatedSubmitRequest{}, fmt.Errorf("invalid entrypoint")
	}
	if request.EntrypointNumCPUs != nil || request.EntrypointNumGPUs != nil || request.EntrypointMemory != nil || len(request.EntrypointResources) != 0 {
		return TranslatedSubmitRequest{}, fmt.Errorf("unsupported entrypoint resources")
	}
	externalID := request.SubmissionID
	if externalID == "" {
		externalID = request.JobID
	}
	if externalID != "" && !submissionID.MatchString(externalID) {
		return TranslatedSubmitRequest{}, fmt.Errorf("invalid submission id")
	}
	packageName, err := parseWorkingDir(request.RuntimeEnv)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	nameSeed := externalID
	if nameSeed == "" {
		nameSeed = packageName.Name + "\\x00" + request.Entrypoint
	}
	nameHash := sha256.Sum256([]byte(nameSeed))
	output := domain.DataLocation{}
	if engine == domain.TrainingEngineRayTrain {
		output = domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: nativeManagedOutputRoot}
	}
	return TranslatedSubmitRequest{
		Package: packageName,
		Spec: domain.JobSpec{
			Name:           "rayjob-" + hex.EncodeToString(nameHash[:])[:24],
			Image:          image,
			TrainingEngine: engine,
			Entrypoint:     domain.Entrypoint{Command: []string{"/bin/sh", "-lc", request.Entrypoint}},
			Execution:      executionProfileForResources(resources),
			Resources:      resources,
			Queue:          queue,
			Cache:          cache,
			Input:          input,
			Output:         output,
			Managed:        managed,
			DataMode:       dataMode,
			DatasetRef:     datasetRef,
			CachePolicy:    cachePolicy,
		},
		ExternalSubmissionID: externalID,
	}, nil
}

func parseDatasetMetadata(metadata map[string]string) (domain.DatasetReference, domain.DatasetCachePolicy, error) {
	known := map[string]struct{}{
		metadataDatasetReference: {}, metadataDatasetVersion: {}, metadataDatasetCachePolicy: {}, "platform.dataset.sites": {},
	}
	for key := range metadata {
		if strings.HasPrefix(key, "platform.dataset.") {
			if _, ok := known[key]; !ok {
				return domain.DatasetReference{}, "", fmt.Errorf("unsupported dataset metadata %q", key)
			}
		}
	}
	ref := domain.DatasetReference{
		Dataset: strings.TrimSpace(metadata[metadataDatasetReference]),
		Version: strings.TrimSpace(metadata[metadataDatasetVersion]),
	}
	if raw, ok := metadata["platform.dataset.sites"]; ok {
		if err := json.Unmarshal([]byte(raw), &ref.Sites); err != nil {
			return domain.DatasetReference{}, "", fmt.Errorf("invalid dataset sites: %w", err)
		}
	}
	policy := domain.DatasetCachePolicy(strings.TrimSpace(metadata[metadataDatasetCachePolicy]))
	if ref.IsZero() && policy == "" {
		return domain.DatasetReference{}, "", nil
	}
	if ref.IsZero() {
		return domain.DatasetReference{}, "", fmt.Errorf("dataset cache policy requires a dataset reference")
	}
	if err := ref.Validate(); err != nil {
		return domain.DatasetReference{}, "", fmt.Errorf("invalid dataset metadata: %w", err)
	}
	if policy == "" {
		policy = domain.DatasetCachePolicyAuto
	}
	if err := policy.Validate(); err != nil {
		return domain.DatasetReference{}, "", fmt.Errorf("invalid dataset metadata: %w", err)
	}
	return ref, policy, nil
}

func parseTrainingMetadata(metadata map[string]string) (domain.TrainingEngine, domain.ManagedTrainingPolicy, error) {
	for key := range metadata {
		if strings.HasPrefix(key, "platform.training.") && key != metadataTrainingEngine {
			return "", domain.ManagedTrainingPolicy{}, fmt.Errorf("unsupported training metadata %q", key)
		}
	}
	engine := domain.TrainingEngine(strings.TrimSpace(metadata[metadataTrainingEngine])).Resolved()
	if engine != domain.TrainingEngineRayDDP && engine != domain.TrainingEngineRayTrain {
		return "", domain.ManagedTrainingPolicy{}, fmt.Errorf("unsupported training engine %q", engine)
	}
	managed := domain.ManagedTrainingPolicy{}
	if engine == domain.TrainingEngineRayTrain {
		managed = domain.ManagedTrainingPolicy{
			MaxFailures: 2,
			Checkpoint:  domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
		}
	}
	return engine, managed, nil
}

func parseCacheMetadata(metadata map[string]string) (domain.CacheRequest, error) {
	for key := range metadata {
		if strings.HasPrefix(key, "platform.cache.") && key != metadataCacheMode && key != metadataCacheSize && key != metadataCachePreload {
			return domain.CacheRequest{}, fmt.Errorf("unsupported cache metadata %q", key)
		}
	}
	mode := domain.CacheMode(strings.TrimSpace(metadata[metadataCacheMode]))
	size := strings.TrimSpace(metadata[metadataCacheSize])
	preload := domain.CachePreloadMode(strings.TrimSpace(metadata[metadataCachePreload]))
	switch mode {
	case "", domain.CacheModeOff:
		if size != "" {
			return domain.CacheRequest{}, fmt.Errorf("off cache cannot specify size")
		}
		if preload != "" {
			return domain.CacheRequest{}, fmt.Errorf("off cache cannot specify preload")
		}
		return domain.CacheRequest{}, nil
	case domain.CacheModeRuntime:
		cache := domain.CacheRequest{Mode: mode, Size: size, Preload: preload}
		if err := cache.Validate(); err != nil && size != "" {
			return domain.CacheRequest{}, err
		}
		if preload != "" && preload != domain.CachePreloadInput {
			return domain.CacheRequest{}, fmt.Errorf("unsupported cache preload %q", preload)
		}
		return cache, nil
	default:
		return domain.CacheRequest{}, fmt.Errorf("unsupported cache mode %q", mode)
	}
}

func parseInputMetadata(metadata map[string]string) (domain.DataLocation, error) {
	for key := range metadata {
		if strings.HasPrefix(key, "platform.data.") && key != metadataInputSpace && key != metadataInputPath {
			return domain.DataLocation{}, fmt.Errorf("unsupported data metadata %q", key)
		}
	}
	space := domain.DataSpaceID(strings.TrimSpace(metadata[metadataInputSpace]))
	relativePath := strings.TrimSpace(metadata[metadataInputPath])
	if space == "" && relativePath == "" {
		return domain.DataLocation{}, nil
	}
	if space == "" || relativePath == "" {
		return domain.DataLocation{}, fmt.Errorf("input space and non-empty input path must be specified together")
	}
	location, err := domain.NewDataLocation(space, relativePath)
	if err != nil {
		return domain.DataLocation{}, fmt.Errorf("invalid input metadata: %w", err)
	}
	return location, nil
}

func executionProfileForResources(resources domain.Resources) domain.ExecutionProfile {
	mode := domain.ExecutionModeSingleGPU
	if resources.WorkerReplicas > 1 {
		mode = domain.ExecutionModeRayTrain
	} else if resources.GPUsPerWorker > 1 {
		mode = domain.ExecutionModeTorchrun
	}
	return domain.ExecutionProfile{Mode: mode}
}

func parseMetadataOrDefaults(metadata map[string]string, defaults SubmissionDefaults) (domain.Resources, string, string, error) {
	if hasResourceMetadata(metadata) {
		return parseMetadata(metadata)
	}
	return defaults.resources()
}

func hasResourceMetadata(metadata map[string]string) bool {
	for key := range metadata {
		if strings.HasPrefix(key, "ray-platform.") {
			return true
		}
	}
	return false
}

func (defaults SubmissionDefaults) resources() (domain.Resources, string, string, error) {
	image := strings.TrimSpace(defaults.Image)
	if err := domain.ValidateRuntimeImage(image); err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("Ray CLI default image is not configured")
	}
	if defaults.WorkerReplicas != 1 || defaults.GPUsPerWorker != 1 || defaults.CPUPerWorker < 1 || defaults.CPUPerWorker > 64 || !validMemory(strings.TrimSpace(defaults.MemoryPerWorker)) {
		return domain.Resources{}, "", "", fmt.Errorf("Ray CLI defaults are invalid")
	}
	return domain.Resources{
		WorkerReplicas: defaults.WorkerReplicas, GPUsPerWorker: defaults.GPUsPerWorker,
		CPUPerWorker: defaults.CPUPerWorker, MemoryPerWorker: strings.TrimSpace(defaults.MemoryPerWorker),
	}, "", image, nil
}

func parseWorkingDir(runtimeEnv map[string]any) (PackageName, error) {
	if runtimeEnv == nil {
		return PackageName{}, fmt.Errorf("runtime environment is required")
	}
	workingDirectory, ok := runtimeEnv["working_dir"].(string)
	if !ok || !strings.HasPrefix(workingDirectory, "gcs://") {
		return PackageName{}, fmt.Errorf("working directory must use gcs")
	}
	return ParsePackageName("gcs", strings.TrimPrefix(workingDirectory, "gcs://"))
}

func parseMetadata(metadata map[string]string) (domain.Resources, string, string, error) {
	if metadata == nil {
		return domain.Resources{}, "", "", fmt.Errorf("metadata is required")
	}
	known := map[string]struct{}{
		metadataImage: {}, metadataWorkerReplicas: {}, metadataGPUsPerWorker: {},
		metadataCPUPerWorker: {}, metadataMemoryWorker: {}, metadataQueue: {},
	}
	for key := range metadata {
		if strings.HasPrefix(key, "ray-platform.") {
			if _, ok := known[key]; !ok {
				return domain.Resources{}, "", "", fmt.Errorf("unsupported platform metadata %q", key)
			}
		}
	}
	image := strings.TrimSpace(metadata[metadataImage])
	if err := domain.ValidateRuntimeImage(image); err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("invalid image")
	}
	workers, err := boundedInt(metadata[metadataWorkerReplicas], 1, 3)
	if err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("invalid worker replicas")
	}
	gpus, err := boundedInt(metadata[metadataGPUsPerWorker], 1, 8)
	if err != nil || workers*gpus > 24 {
		return domain.Resources{}, "", "", fmt.Errorf("invalid GPUs per worker")
	}
	cpu, err := boundedInt64(metadata[metadataCPUPerWorker], 1, 64)
	if err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("invalid CPU per worker")
	}
	memory := strings.TrimSpace(metadata[metadataMemoryWorker])
	if !validMemory(memory) {
		return domain.Resources{}, "", "", fmt.Errorf("invalid memory per worker")
	}
	queue := strings.TrimSpace(metadata[metadataQueue])
	if len(queue) > 63 || !dnsLabel.MatchString(queue) {
		return domain.Resources{}, "", "", fmt.Errorf("invalid queue")
	}
	return domain.Resources{WorkerReplicas: workers, GPUsPerWorker: gpus, CPUPerWorker: cpu, MemoryPerWorker: memory}, queue, image, nil
}

func boundedInt(value string, minimum, maximum int) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum || strconv.Itoa(parsed) != value {
		return 0, fmt.Errorf("out of range")
	}
	return parsed, nil
}

func boundedInt64(value string, minimum, maximum int64) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum || strconv.FormatInt(parsed, 10) != value {
		return 0, fmt.Errorf("out of range")
	}
	return parsed, nil
}

func validMemory(value string) bool {
	matches := memoryQuantity.FindStringSubmatch(value)
	if matches == nil {
		return false
	}
	amount, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || amount < 1 {
		return false
	}
	if matches[2] == "Gi" {
		return amount <= 1024
	}
	return amount <= 1024*1024
}

func rayPackageArtifactID(tenantID, userID, packageName string) string {
	sum := sha256.Sum256([]byte(tenantID + "\\x00" + userID + "\\x00" + packageName))
	return "raypkg-" + hex.EncodeToString(sum[:])
}
