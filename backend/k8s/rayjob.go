package k8s

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

const (
	RayAPIVersion  = "ray.io/v1"
	RayJobKind     = "RayJob"
	RayJobResource = "rayjobs"
)

type RenderOptions struct {
	ClusterSpecField        string
	RayVersion              string
	ServiceAccount          string
	ImagePullSecrets        []string
	SourceMaterializerImage string
	IDCExistingClaim        string
	IDCMountPath            string
	LocalCache              LocalCacheOptions
	// NodeSelector pins Ray Pods to the GPU training pool. It is configuration
	// so that adding machines or changing GPU model needs no code change.
	NodeSelector map[string]string
	// GitCredentialSecret names a Secret in the tenant namespace holding
	// GIT_USERNAME/GIT_TOKEN for a private repository. Empty for public ones.
	GitCredentialSecret string
	MLflow              MLflowOptions
}

// MLflowOptions carries only non-secret, in-cluster routing information.
// Object-store credentials remain attached to the MLflow server itself.
type MLflowOptions struct {
	Enabled          bool
	TrackingURI      string
	ExperimentPrefix string
	ProvenanceKey    []byte
	jobID            string
	tenantID         string
	userID           string
}

// LocalCacheOptions describes disposable node-local storage for a single Ray
// Pod. It is deliberately not part of the storage catalog: neither input data
// nor output artifacts may depend on its lifecycle.
type LocalCacheOptions struct {
	Enabled      bool
	StorageClass string
	AllowedSizes []string
	DefaultSize  string
	MaxSize      string
	MountPath    string
	runtime      bool
	resolvedSize string
}

// defaultTrainingNodeSelector matches the label the deployment guide asks
// operators to put on training nodes.
var defaultTrainingNodeSelector = map[string]string{"accelerator": "nvidia-rtx-4090"}

func trainingNodeSelector(options RenderOptions) map[string]any {
	source := options.NodeSelector
	if len(source) == 0 {
		source = defaultTrainingNodeSelector
	}
	selector := make(map[string]any, len(source))
	for key, value := range source {
		selector[key] = value
	}
	return selector
}

func RenderRayJob(job domain.TrainingJob, options RenderOptions) (*unstructured.Unstructured, error) {
	if err := job.Spec.Validate(); err != nil {
		return nil, fmt.Errorf("validate job spec: %w", err)
	}
	if err := domain.ValidatePinnedImage(options.SourceMaterializerImage); err != nil {
		return nil, fmt.Errorf("source materializer image: %w", err)
	}
	if err := options.LocalCache.Validate(); err != nil {
		return nil, fmt.Errorf("local cache: %w", err)
	}
	localCache, err := options.LocalCache.resolve(job.Spec.Cache)
	if err != nil {
		return nil, fmt.Errorf("local cache: %w", err)
	}
	options.LocalCache = localCache
	if err := options.MLflow.Validate(); err != nil {
		return nil, fmt.Errorf("MLflow: %w", err)
	}
	if job.Spec.Source.Type != "git" && job.Spec.Source.Type != "workspace" && job.Spec.Source.Type != "workspace-archive" {
		// Defense in depth for callers that bypass the HTTP submission service.
		// Ray workloads must not receive object-store credentials just to obtain
		// their source code.
		return nil, fmt.Errorf("unsupported Ray workload code source %q", job.Spec.Source.Type)
	}
	if (job.Spec.Source.Type == "workspace" || job.Spec.Source.Type == "workspace-archive") && job.Spec.ResolvedDataRoots.Personal == nil {
		return nil, fmt.Errorf("workspace-derived source requires a resolved personal data mount")
	}
	if job.Spec.Source.Type == "workspace-archive" {
		if strings.TrimSpace(job.Spec.Source.ArtifactID) == "" || strings.TrimSpace(job.Spec.Source.ArtifactSHA256) == "" {
			return nil, fmt.Errorf("workspace archive source must be materialized before rendering")
		}
		if !domain.IsSourceArtifactObjectKeyForTenant(job.TenantID, job.Spec.Source.ArtifactObjectKey, job.Spec.Source.ArtifactSHA256) {
			return nil, fmt.Errorf("workspace archive source is not owner scoped")
		}
	}
	namespace := strings.TrimSpace(job.KubernetesNS)
	if namespace == "" {
		namespace = "tenant-" + sanitizeDNS(job.TenantID)
	}
	if !isDNSLabel(namespace) {
		return nil, fmt.Errorf("kubernetes namespace must be a lowercase DNS label")
	}
	clusterSpecField := options.ClusterSpecField
	if clusterSpecField == "" {
		clusterSpecField = "rayClusterSpec"
	}
	if clusterSpecField != "rayClusterConfig" && clusterSpecField != "rayClusterSpec" {
		return nil, fmt.Errorf("unsupported RayJob cluster spec field %q", clusterSpecField)
	}
	rayVersion := strings.TrimSpace(options.RayVersion)
	if rayVersion == "" {
		rayVersion = "2.35.0"
	}
	workerReplicas := int64(job.Spec.Resources.WorkerReplicas)
	gpusPerWorker := int64(job.Spec.Resources.GPUsPerWorker)
	workerCPU := strconv.FormatInt(job.Spec.Resources.CPUPerWorker, 10)
	if job.Spec.Resources.CPUPerWorker <= 0 {
		workerCPU = "8"
	}
	workerMemory := job.Spec.Resources.MemoryPerWorker
	if strings.TrimSpace(workerMemory) == "" {
		workerMemory = "32Gi"
	}
	entrypoint := executionProfileEntrypoint(job.Spec)
	options.MLflow.jobID = job.ID
	options.MLflow.tenantID = job.TenantID
	options.MLflow.userID = job.UserID

	// The submitter runs `ray job submit`, and Ray uploads the runtime env's
	// working_dir from the submitter's own filesystem. Materialize the source
	// only there: Ray distributes that runtime env to the driver and workers.
	// This prevents every Pod from independently fetching the same Git source
	// and keeps private Git credentials out of the Ray cluster Pods.
	submitterPod := podTemplate("ray-job-submitter", job.Spec.Image, "1", "2Gi", 0, job.Spec.Source, job.Spec, options, true, false, true)
	// KubeRay wraps this template in a batch Job, whose pod spec requires an
	// explicit restartPolicy; without it the submitter Job is rejected and the
	// RayJob stalls in Initializing forever.
	submitterPod["spec"].(map[string]any)["restartPolicy"] = "Never"
	addPodLabels(submitterPod, job.ID, job.TenantID)
	headPod := podTemplate("ray-head", job.Spec.Image, "4", "16Gi", 0, job.Spec.Source, job.Spec, options, true, true, false)
	workerPod := podTemplate("ray-worker", job.Spec.Image, workerCPU, workerMemory, gpusPerWorker, job.Spec.Source, job.Spec, options, false, true, false)
	addPodLabels(headPod, job.ID, job.TenantID)
	addPodLabels(workerPod, job.ID, job.TenantID)
	if job.Spec.Execution.ResolvedMode() == domain.ExecutionModeRayTrain {
		// A Ray-managed DDP worker represents one physical training node. Require
		// an even host spread so a multi-worker submission is a real multi-node
		// validation rather than two worker Pods packed onto one 8-GPU server.
		workerPod["spec"].(map[string]any)["topologySpreadConstraints"] = []any{map[string]any{
			"maxSkew":           int64(1),
			"minDomains":        int64(2),
			"topologyKey":       "kubernetes.io/hostname",
			"whenUnsatisfiable": "DoNotSchedule",
			"labelSelector": map[string]any{"matchLabels": map[string]any{
				"platform_job_id": job.ID,
			}},
		}}
	}
	headStartParams := map[string]any{"dashboard-host": "0.0.0.0", "num-gpus": "0"}
	workerStartParams := map[string]any{"num-gpus": strconv.FormatInt(gpusPerWorker, 10)}
	if options.LocalCache.runtime {
		configureRayCache(headStartParams, options.LocalCache)
		configureRayCache(workerStartParams, options.LocalCache)
	}
	clusterSpec := map[string]any{
		"rayVersion": rayVersion,
		"headGroupSpec": map[string]any{
			"rayStartParams": headStartParams,
			"template":       headPod,
		},
		"workerGroupSpecs": []any{map[string]any{
			"groupName":      "worker-group",
			"replicas":       workerReplicas,
			"minReplicas":    workerReplicas,
			"maxReplicas":    workerReplicas,
			"rayStartParams": workerStartParams,
			"template":       workerPod,
		}},
	}
	labels := map[string]any{
		"app.kubernetes.io/part-of":    "ray-train-platform",
		"app.kubernetes.io/managed-by": "ray-train-platform",
		"ray.io/job-id":                job.ID,
		"ray.io/tenant-id":             job.TenantID,
		"platform_job_id":              job.ID,
		"platform_tenant_id":           job.TenantID,
		"kueue.x-k8s.io/queue-name":    job.Spec.Queue,
	}
	jobObject := map[string]any{
		"apiVersion": RayAPIVersion,
		"kind":       RayJobKind,
		"metadata": map[string]any{
			// The display name is reusable. Kubernetes identity comes from the
			// immutable platform job ID so repeated project runs never collide.
			"name":      rayJobResourceName(job),
			"namespace": namespace,
			"labels":    labels,
			"annotations": map[string]any{
				"ray-train-platform/job-id": job.ID,
				"ray-train-platform/owner":  job.UserID,
			},
		},
		"spec": jobSpecFields(job, clusterSpecField, clusterSpec, entrypoint, submitterPod),
	}
	return &unstructured.Unstructured{Object: jobObject}, nil
}

// executionProfileEntrypoint keeps compatibility for persisted V1 jobs while routing
// newly selected profiles through the image-owned launcher. The launcher is
// deliberately part of the immutable runtime image: neither API clients nor
// user code need to understand Ray head/worker placement or credentials.
func executionProfileEntrypoint(spec domain.JobSpec) []string {
	command := append(append([]string{}, spec.Entrypoint.Command...), spec.Entrypoint.Args...)
	mode := spec.Execution.ResolvedMode()
	if mode == domain.ExecutionModeLegacy {
		return command
	}
	launcher := []string{
		"raytrain-launch",
		"--mode", string(mode),
		"--workers", strconv.Itoa(spec.Resources.WorkerReplicas),
		"--gpus-per-worker", strconv.Itoa(spec.Resources.GPUsPerWorker),
		"--",
	}
	return append(launcher, command...)
}

func (options LocalCacheOptions) Validate() error {
	if !options.Enabled {
		return nil
	}
	if !isDNSSubdomain(strings.TrimSpace(options.StorageClass)) {
		return fmt.Errorf("storage class must be a valid Kubernetes name")
	}
	maximum, err := positiveCacheQuantity(options.MaxSize)
	if err != nil {
		return fmt.Errorf("maximum size must be a positive Kubernetes storage quantity")
	}
	defaultSize, err := positiveCacheQuantity(options.DefaultSize)
	if err != nil {
		return fmt.Errorf("default size must be a positive Kubernetes storage quantity")
	}
	if len(options.AllowedSizes) == 0 {
		return fmt.Errorf("allowed sizes must not be empty")
	}
	allowed := make([]resource.Quantity, 0, len(options.AllowedSizes))
	defaultAllowed := false
	for _, configured := range options.AllowedSizes {
		quantity, err := positiveCacheQuantity(configured)
		if err != nil {
			return fmt.Errorf("allowed sizes must be positive Kubernetes storage quantities")
		}
		if quantity.Cmp(maximum) > 0 {
			return fmt.Errorf("allowed size %q exceeds maximum %q", configured, options.MaxSize)
		}
		for _, existing := range allowed {
			if quantity.Cmp(existing) == 0 {
				return fmt.Errorf("allowed sizes must be unique")
			}
		}
		allowed = append(allowed, quantity)
		defaultAllowed = defaultAllowed || quantity.Cmp(defaultSize) == 0
	}
	if !defaultAllowed {
		return fmt.Errorf("default size must belong to allowed sizes")
	}
	mountPath := strings.TrimSpace(options.MountPath)
	if !strings.HasPrefix(mountPath, "/") || path.Clean(mountPath) != mountPath || mountPath == "/" {
		return fmt.Errorf("mount path must be a clean absolute directory")
	}
	if mountPath == "/tmp/ray" || strings.HasPrefix(mountPath, "/tmp/ray/") {
		return fmt.Errorf("mount path must not be inside Ray's default temporary directory")
	}
	return nil
}

func (options LocalCacheOptions) resolve(request domain.CacheRequest) (LocalCacheOptions, error) {
	options.AllowedSizes = append([]string(nil), options.AllowedSizes...)
	options.runtime = false
	options.resolvedSize = ""
	mode := request.Mode
	if mode == "" {
		mode = domain.CacheModeOff
	}
	if mode == domain.CacheModeOff {
		return options, nil
	}
	if mode != domain.CacheModeRuntime {
		return LocalCacheOptions{}, fmt.Errorf("unsupported cache mode %q", mode)
	}
	if !options.Enabled {
		return LocalCacheOptions{}, fmt.Errorf("runtime cache capability is disabled")
	}
	requested, err := positiveCacheQuantity(request.Size)
	if err != nil {
		return LocalCacheOptions{}, fmt.Errorf("runtime cache size must be a positive Kubernetes storage quantity")
	}
	maximum, _ := positiveCacheQuantity(options.MaxSize)
	if requested.Cmp(maximum) > 0 {
		return LocalCacheOptions{}, fmt.Errorf("runtime cache size %q exceeds maximum %q", request.Size, options.MaxSize)
	}
	for _, configured := range options.AllowedSizes {
		allowed, parseErr := positiveCacheQuantity(configured)
		if parseErr == nil && requested.Cmp(allowed) == 0 {
			options.runtime = true
			options.resolvedSize = strings.TrimSpace(configured)
			return options, nil
		}
	}
	return LocalCacheOptions{}, fmt.Errorf("runtime cache size %q is not allowed", request.Size)
}

func positiveCacheQuantity(value string) (resource.Quantity, error) {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
	if err != nil || quantity.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("quantity must be positive")
	}
	return quantity, nil
}

func configureRayCache(rayStartParams map[string]any, cache LocalCacheOptions) {
	rayStartParams["temp-dir"] = path.Join(cache.MountPath, "ray")
	rayStartParams["object-spilling-config"] = rayObjectSpillingConfig(path.Join(cache.MountPath, "ray-spill", "objects"))
}

func rayObjectSpillingConfig(directory string) string {
	config, _ := json.Marshal(map[string]any{
		"type":   "filesystem",
		"params": map[string]string{"directory_path": directory},
	})
	return string(config)
}

const (
	// A successful run has already persisted its history and artifacts, so its
	// scarce GPU workers can be released promptly.
	defaultSuccessCleanupTTLSeconds int64 = 60
	// Failed runs retain a longer native Ray diagnostics window. The reconciler
	// shortens this value only after it observes SUCCEEDED.
	defaultFailureCleanupTTLSeconds int64 = 600
)

func rayJobResourceName(job domain.TrainingJob) string {
	// Jobs created before display names became reusable already have a live
	// Kubernetes resource under the persisted name. Keep using that exact
	// identity so an upgrade cannot create a duplicate workload.
	if persisted := strings.TrimSpace(job.RayJobName); persisted != "" {
		return persisted
	}
	return sanitizeDNS(job.ID)
}

func successCleanupTTL(job domain.TrainingJob) int64 {
	ttl := job.Spec.CleanupPolicy.SuccessTTLSeconds
	if ttl <= 0 {
		return defaultSuccessCleanupTTLSeconds
	}
	return ttl
}

func failureCleanupTTL(job domain.TrainingJob) int64 {
	ttl := job.Spec.CleanupPolicy.FailureTTLSeconds
	if ttl <= 0 {
		return defaultFailureCleanupTTLSeconds
	}
	return ttl
}

func jobSpecFields(job domain.TrainingJob, clusterSpecField string, clusterSpec map[string]any, entrypoint []string, submitterPod map[string]any) map[string]any {
	spec := map[string]any{
		"submissionMode": "K8sJobMode",
		// KubeRay appends this to `ray job submit -- ...`. It must be a single
		// command with no shell operators: a "cd /workspace &&" prefix would end
		// the submitted command, so Ray would run only the cd, report SUCCEEDED,
		// and the real training would execute in the submitter pod instead.
		// The working directory comes from the runtime env below, which is also
		// what ships the materialized source to the driver and workers.
		"entrypoint":     shellJoin(entrypoint),
		"runtimeEnvYAML": "working_dir: /workspace\nenv_vars:\n  PYTHONUNBUFFERED: \"1\"\n",
		clusterSpecField: clusterSpec,
		// Release the GPUs as soon as the run ends; without this the RayCluster
		// outlives the job and the worker Pods keep their nvidia.com/gpu claims.
		"shutdownAfterJobFinishes": true,
		// KubeRay has one TTL for every terminal state. Start conservatively
		// with the failure window; the reconciler changes it after SUCCEEDED.
		"ttlSecondsAfterFinished": failureCleanupTTL(job),
		// Kueue admits a workload by clearing suspend. A job created unsuspended
		// would start immediately and bypass the tenant GPU quota. JobSpec
		// validation guarantees a queue, so this always applies.
		"suspend":              true,
		"submitterPodTemplate": submitterPod,
	}
	// KubeRay creates the submitter as a Kubernetes Job. Its backoff limit
	// covers transient provisioning failures only (image pull, temporary API
	// or node interruptions). A training process that exits non-zero still
	// requires an explicit user-confirmed resume from a persisted checkpoint.
	if job.Spec.RetryPolicy.MaxRetries > 0 {
		spec["submitterConfig"] = map[string]any{"backoffLimit": int64(job.Spec.RetryPolicy.MaxRetries)}
	}
	if job.Spec.TimeoutSeconds > 0 {
		spec["activeDeadlineSeconds"] = job.Spec.TimeoutSeconds
	}
	return spec
}

func podTemplate(containerName, image, cpu, memory string, gpus int64, source domain.CodeSource, jobSpec domain.JobSpec, options RenderOptions, head, mountData, materializeSource bool) map[string]any {
	resources := map[string]any{
		"requests": map[string]any{"cpu": cpu, "memory": memory},
		"limits":   map[string]any{"cpu": cpu, "memory": memory},
	}
	if gpus > 0 {
		resources["requests"].(map[string]any)["nvidia.com/gpu"] = strconv.FormatInt(gpus, 10)
		resources["limits"].(map[string]any)["nvidia.com/gpu"] = strconv.FormatInt(gpus, 10)
	}
	env := []any{
		map[string]any{"name": "PYTHONUNBUFFERED", "value": "1"},
		map[string]any{"name": "RAY_DISABLE_DOCKER_CPU_WARNING", "value": "1"},
	}
	if mountData {
		env = append(env, platformDataEnvironment(jobSpec)...)
	}
	if options.MLflow.Enabled {
		env = append(env, mlflowEnvironment(options.MLflow)...)
	}
	if !head {
		env = append(env,
			map[string]any{"name": "NCCL_P2P_DISABLE", "value": "1"},
			map[string]any{"name": "NCCL_IB_DISABLE", "value": "1"},
			map[string]any{"name": "NCCL_DEBUG", "value": "WARN"},
		)
	}
	volumeMounts := []any{
		map[string]any{"name": "workspace", "mountPath": "/workspace"},
		map[string]any{"name": "dshm", "mountPath": "/dev/shm"},
	}
	volumes := []any{
		map[string]any{"name": "workspace", "emptyDir": map[string]any{}},
		map[string]any{"name": "dshm", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "32Gi"}},
	}
	if !options.LocalCache.runtime {
		volumeMounts = append(volumeMounts, map[string]any{"name": "ray-spill", "mountPath": "/tmp/ray-spill"})
		volumes = append(volumes, map[string]any{"name": "ray-spill", "emptyDir": map[string]any{}})
	}
	useLegacyIDC := options.IDCExistingClaim != "" && !hasAnyResolvedDataRoots(jobSpec.ResolvedDataRoots)
	if useLegacyIDC {
		mountPath := options.IDCMountPath
		if mountPath == "" {
			mountPath = "/mnt/idc"
		}
		volumeMounts = append(volumeMounts, map[string]any{"name": "idc-storage", "mountPath": mountPath, "readOnly": true})
		volumes = append(volumes, map[string]any{
			"name":                  "idc-storage",
			"persistentVolumeClaim": map[string]any{"claimName": options.IDCExistingClaim, "readOnly": true},
		})
	}
	if mountData {
		volumeMounts, volumes = appendResolvedStorageMounts(volumeMounts, volumes, jobSpec.ResolvedStorage)
		volumeMounts, volumes = appendTrainingDataRoots(volumeMounts, volumes, jobSpec.ResolvedDataRoots)
		volumeMounts, volumes = appendResolvedDataSpaceMounts(volumeMounts, volumes, jobSpec.ResolvedDataMounts)
	}
	if materializeSource && (source.Type == "workspace" || source.Type == "workspace-archive") {
		personal := jobSpec.ResolvedDataRoots.Personal
		if personal != nil {
			volumes = append(volumes, pvcVolume("workspace-snapshot-source", personal.ClaimName, true))
		}
	}
	if mountData && options.LocalCache.runtime {
		volumeMounts, volumes = appendLocalCache(volumeMounts, volumes, options.LocalCache)
		env = append(env, map[string]any{"name": "PLATFORM_CACHE_PATH", "value": options.LocalCache.MountPath})
	}
	podSpec := map[string]any{
		"serviceAccountName":           options.ServiceAccount,
		"automountServiceAccountToken": options.ServiceAccount != "",
		"securityContext":              map[string]any{"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers": []any{map[string]any{
			"name":            containerName,
			"image":           image,
			"imagePullPolicy": domain.RuntimeImagePullPolicy(image),
			"workingDir":      "/workspace",
			"resources":       resources,
			"env":             env,
			"volumeMounts":    volumeMounts,
			"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
		}},
		"volumes": volumes,
	}
	if materializeSource {
		podSpec["initContainers"] = []any{sourceMaterializer(source, jobSpec, options)}
	}
	if mountData && options.LocalCache.runtime {
		podSpec["securityContext"].(map[string]any)["fsGroup"] = int64(1000)
	}
	if pullSecrets := renderImagePullSecrets(options.ImagePullSecrets); len(pullSecrets) > 0 {
		podSpec["imagePullSecrets"] = pullSecrets
	}
	// Both head and workers stay on the real training pool: a head scheduled
	// onto a serverless virtual node cannot host the GCS for the workers.
	podSpec["nodeSelector"] = trainingNodeSelector(options)
	if podSpec["serviceAccountName"] == "" {
		delete(podSpec, "serviceAccountName")
	}
	return map[string]any{"spec": podSpec}
}

func (options MLflowOptions) Validate() error {
	if !options.Enabled {
		return nil
	}
	if strings.TrimSpace(options.TrackingURI) == "" {
		return fmt.Errorf("tracking URI is required when enabled")
	}
	prefix := strings.Trim(strings.TrimSpace(options.ExperimentPrefix), "-")
	if prefix == "" {
		return fmt.Errorf("experiment prefix is required when enabled")
	}
	if len(options.ProvenanceKey) < 32 {
		return fmt.Errorf("provenance key must contain at least 32 bytes")
	}
	return nil
}

func mlflowProvenanceTag(key []byte, jobID string) string {
	return domain.MLflowProvenanceTag(key, jobID)
}

func mlflowEnvironment(options MLflowOptions) []any {
	return []any{
		map[string]any{"name": "MLFLOW_TRACKING_URI", "value": options.TrackingURI},
		map[string]any{"name": "MLFLOW_EXPERIMENT_NAME", "value": strings.Trim(options.ExperimentPrefix, "-") + "-" + options.tenantID},
		map[string]any{"name": "MLFLOW_RUN_NAME", "value": options.jobID},
		map[string]any{"name": "RAYTRAIN_JOB_ID", "value": options.jobID},
		map[string]any{"name": "RAYTRAIN_TENANT_ID", "value": options.tenantID},
		map[string]any{"name": "RAYTRAIN_SUBMITTER_USER_ID", "value": options.userID},
		map[string]any{"name": "RAYTRAIN_MLFLOW_PROVENANCE", "value": mlflowProvenanceTag(options.ProvenanceKey, options.jobID)},
	}
}

func appendLocalCache(volumeMounts, volumes []any, cache LocalCacheOptions) ([]any, []any) {
	volumeMounts = append(volumeMounts, map[string]any{"name": "local-cache", "mountPath": cache.MountPath})
	volumes = append(volumes, map[string]any{
		"name": "local-cache",
		"ephemeral": map[string]any{
			"volumeClaimTemplate": map[string]any{
				"spec": map[string]any{
					"accessModes":      []any{"ReadWriteOnce"},
					"storageClassName": cache.StorageClass,
					"resources": map[string]any{
						"requests": map[string]any{"storage": cache.resolvedSize},
					},
				},
			},
		},
	})
	return volumeMounts, volumes
}

func platformDataEnvironment(spec domain.JobSpec) []any {
	locations := []struct {
		name  string
		value string
	}{
		{name: "PLATFORM_DATASET_URI", value: spec.DatasetURI},
		{name: "PLATFORM_CHECKPOINT_URI", value: spec.CheckpointURI},
		{name: "PLATFORM_OUTPUT_URI", value: spec.OutputURI},
	}
	env := make([]any, 0, len(locations))
	for _, location := range locations {
		if value := strings.TrimSpace(location.value); value != "" {
			env = append(env, map[string]any{"name": location.name, "value": value})
		}
	}
	mounts := []struct {
		name  string
		mount *domain.ResolvedStorageMount
	}{
		{name: "PLATFORM_DATASET_PATH", mount: spec.ResolvedStorage.Dataset},
		{name: "PLATFORM_CHECKPOINT_PATH", mount: spec.ResolvedStorage.Checkpoint},
		{name: "PLATFORM_OUTPUT_PATH", mount: spec.ResolvedStorage.Output},
	}
	for _, item := range mounts {
		if item.mount == nil {
			continue
		}
		path := item.mount.MountPath
		if item.mount.RelativePath != "" {
			path += "/" + item.mount.RelativePath
		}
		env = append(env, map[string]any{"name": item.name, "value": path})
	}
	governedMounts := []struct {
		name  string
		mount *domain.ResolvedDataMount
	}{
		{name: "PLATFORM_DATASET_PATH", mount: spec.ResolvedDataMounts.Input},
		{name: "PLATFORM_CHECKPOINT_PATH", mount: spec.ResolvedDataMounts.Checkpoint},
		{name: "PLATFORM_OUTPUT_PATH", mount: spec.ResolvedDataMounts.Output},
	}
	for _, item := range governedMounts {
		if item.mount == nil {
			continue
		}
		env = append(env, map[string]any{"name": item.name, "value": item.mount.MountPath})
	}
	return env
}

func appendResolvedStorageMounts(volumeMounts, volumes []any, mounts domain.ResolvedStorageMounts) ([]any, []any) {
	items := []struct {
		volumeName string
		mount      *domain.ResolvedStorageMount
	}{
		{volumeName: "platform-dataset", mount: mounts.Dataset},
		{volumeName: "platform-checkpoint", mount: mounts.Checkpoint},
		{volumeName: "platform-output", mount: mounts.Output},
	}
	for _, item := range items {
		if item.mount == nil {
			continue
		}
		volumeMounts = append(volumeMounts, map[string]any{
			"name": item.volumeName, "mountPath": item.mount.MountPath, "readOnly": item.mount.ReadOnly,
		})
		volumes = append(volumes, map[string]any{
			"name": item.volumeName,
			"persistentVolumeClaim": map[string]any{
				"claimName": item.mount.ClaimName, "readOnly": item.mount.ReadOnly,
			},
		})
	}
	return volumeMounts, volumes
}

// appendResolvedDataSpaceMounts renders only control-plane-generated mount
// contracts. The subPath keeps each Pod inside its selected logical directory
// even though the backing PVC is a wider platform-managed root.
func appendResolvedDataSpaceMounts(volumeMounts, volumes []any, mounts domain.ResolvedDataSpaceMounts) ([]any, []any) {
	items := []struct {
		volumeName string
		mount      *domain.ResolvedDataMount
	}{
		{volumeName: "platform-data-input", mount: mounts.Input},
		{volumeName: "platform-data-checkpoint", mount: mounts.Checkpoint},
		{volumeName: "platform-data-output", mount: mounts.Output},
	}
	for _, item := range items {
		if item.mount == nil {
			continue
		}
		volumeName := item.volumeName
		// A tenant-root data PVC can already be mounted at governed
		// personal/team/public paths. Reuse that one writable PVC source for
		// readonly selections backed by that claim; only the individual
		// container mount carries the read-only flag. Declaring the same FSX
		// PVC twice, once read-write and once read-only, causes the CSI driver
		// to publish the shared source read-only for the whole Pod. Tenant-root
		// output subpaths also reuse that source; legacy per-user output paths
		// retain their named volume for compatibility.
		if existing := pvcVolumeName(volumes, item.mount.ClaimName); existing == "platform-data-personal" && (item.mount.ReadOnly || strings.HasPrefix(item.mount.SubPath, "tenants/")) {
			volumeName = existing
		} else {
			volumes = append(volumes, map[string]any{
				"name": item.volumeName,
				"persistentVolumeClaim": map[string]any{
					"claimName": item.mount.ClaimName, "readOnly": item.mount.ReadOnly,
				},
			})
		}
		mount := map[string]any{
			"name": volumeName, "mountPath": item.mount.MountPath, "readOnly": item.mount.ReadOnly,
		}
		if item.mount.SubPath != "" {
			mount["subPath"] = item.mount.SubPath
		}
		volumeMounts = append(volumeMounts, mount)
	}
	return volumeMounts, volumes
}

func renderImagePullSecrets(names []string) []any {
	pullSecrets := make([]any, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			pullSecrets = append(pullSecrets, map[string]any{"name": strings.TrimSpace(name)})
		}
	}
	return pullSecrets
}

func hasGovernedIDCDataRoots(roots domain.ResolvedDataSpaceRoots) bool {
	return roots.IDCOriginal != nil || roots.IDCWellspiking != nil || roots.IDCShared != nil
}

func hasAnyResolvedDataRoots(roots domain.ResolvedDataSpaceRoots) bool {
	return roots.Personal != nil || roots.Team != nil || roots.Public != nil || hasGovernedIDCDataRoots(roots)
}

func sourceMaterializer(source domain.CodeSource, jobSpec domain.JobSpec, options RenderOptions) map[string]any {
	command := "set -eu\nfind /workspace -mindepth 1 -maxdepth 1 -exec rm -rf {} +\n"
	switch source.Type {
	case "git":
		// The workspace volume is created by the kubelet and is not owned by the
		// container user, so git aborts with "detected dubious ownership".
		// safe.directory is passed per command rather than written with
		// --global because the materializer image has no writable HOME.
		git := "git -c safe.directory=/workspace"
		// Never let Git open a terminal prompt in a Pod. A hard timeout keeps a
		// broken or half-open Git connection from holding the RayJob in its
		// provisioning phase forever; the low-speed guard fails stalled streams
		// sooner while still allowing normal large repository transfers.
		command += "export GIT_TERMINAL_PROMPT=0\n"
		if options.GitCredentialSecret != "" {
			// Feed the token through an askpass helper. Putting it in the remote
			// URL would persist it in .git/config and expose it in process
			// arguments and error messages.
			command += "printf '#!/bin/sh\\ncase \"$1\" in *Username*) echo \"$GIT_USERNAME\";; *) echo \"$GIT_TOKEN\";; esac\\n' > /tmp/askpass\n"
			command += "chmod +x /tmp/askpass\n"
			command += "export GIT_ASKPASS=/tmp/askpass\n"
		}
		command += git + " init /workspace\n"
		command += git + " -C /workspace remote add origin " + shellQuote(source.URL) + "\n"
		command += "if ! timeout 180 " + git + " -c http.lowSpeedLimit=1024 -c http.lowSpeedTime=60 -C /workspace fetch --depth 1 origin " + shellQuote(source.Commit) + "; then\n"
		command += "  echo 'source materialization failed: Git fetch failed or exceeded 180 seconds' >&2\n"
		command += "  exit 1\n"
		command += "fi\n"
		command += git + " -C /workspace checkout --detach FETCH_HEAD\n"
	case "workspace":
		// /workspace is an emptyDir created by kubelet and its root is not owned
		// by the non-root materializer. Copy contents only: preserving ownership,
		// modes or timestamps of that root makes cp return EPERM after the files
		// were copied, which causes a needless submitter retry.
		command += "cp -R " + shellQuote("/mnt/platform-workspace-snapshot/snapshots/"+source.Snapshot) + "/. /workspace/\n"
	case "workspace-archive":
		command += "python3 /usr/local/bin/platform-safe-extract.py --archive " + shellQuote("/mnt/platform-workspace-snapshot/workspace/.ray-train-archives/"+source.ArtifactSHA256+".zip") + " --destination /workspace\n"
	}
	env := []any{}
	if source.Type == "git" && options.GitCredentialSecret != "" {
		for _, key := range []string{"GIT_USERNAME", "GIT_TOKEN"} {
			env = append(env, map[string]any{
				"name": key,
				"valueFrom": map[string]any{"secretKeyRef": map[string]any{
					"name": options.GitCredentialSecret, "key": key,
				}},
			})
		}
	}
	volumeMounts := []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}
	if source.Type == "workspace" || source.Type == "workspace-archive" {
		mount := map[string]any{"name": "workspace-snapshot-source", "mountPath": "/mnt/platform-workspace-snapshot", "readOnly": true}
		if personal := jobSpec.ResolvedDataRoots.Personal; personal != nil && personal.SubPath != "" {
			mount["subPath"] = personal.SubPath
		}
		volumeMounts = append(volumeMounts, mount)
	}
	if source.Type == "workspace" && jobSpec.ResolvedDataRoots.Personal == nil {
		// RenderRayJob validates this before templates are generated. Keep the
		// init container defensive for direct unit-level calls.
		volumeMounts = []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}
	}
	return map[string]any{
		"name":            "source-materializer",
		"image":           options.SourceMaterializerImage,
		"imagePullPolicy": "IfNotPresent",
		"command":         []any{"/bin/sh", "-c"},
		"args":            []any{command},
		"env":             env,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
		},
		"securityContext": map[string]any{
			"runAsNonRoot":             true,
			"runAsUser":                int64(1000),
			"runAsGroup":               int64(1000),
			"allowPrivilegeEscalation": false,
			"capabilities":             map[string]any{"drop": []any{"ALL"}},
		},
		"volumeMounts": volumeMounts,
	}
}

func addPodLabels(template map[string]any, jobID, tenantID string) {
	template["metadata"] = map[string]any{"labels": map[string]any{
		"app.kubernetes.io/part-of": "ray-train-platform",
		"platform_job_id":           jobID,
		"platform_tenant_id":        tenantID,
	}}
}

func MapRayJobStatus(jobID string, status map[string]any, resourceVersion string) domain.ObservedJobState {
	jobStatus := strings.ToUpper(stringValue(status, "jobStatus"))
	if jobStatus == "" {
		jobStatus = strings.ToUpper(stringValue(status, "jobDeploymentStatus"))
	}
	state := domain.StateProvisioning
	switch jobStatus {
	case "":
		state = domain.StateQueued
	case "RUNNING", "STARTED":
		state = domain.StateRunning
	case "SUCCEEDED", "SUCCESS", "COMPLETED":
		state = domain.StateSucceeded
	case "FAILED", "ERROR":
		state = domain.StateFailed
	case "STOPPED", "CANCELED", "CANCELLED":
		state = domain.StateCanceled
	case "PENDING", "WAITING":
		state = domain.StateQueued
	case "SUSPENDED":
		// Kueue suspends a RayJob until a matching admission is available. It is
		// queued, not an unknown or failed training job.
		state = domain.StateQueued
	case "PROVISIONING", "DEPLOYING", "INITIALIZING":
		state = domain.StateProvisioning
	default:
		state = domain.StateUnknown
	}
	reason := stringValue(status, "reason")
	message := stringValue(status, "message")
	if message == "" {
		message = stringValue(status, "jobStatus")
	}
	if message == "" {
		message = stringValue(status, "jobDeploymentStatus")
	}
	return domain.ObservedJobState{
		ID:              jobID,
		State:           state,
		Reason:          reason,
		Message:         message,
		RayJobUID:       stringValue(status, "rayJobUID"),
		RayClusterName:  stringValue(status, "rayClusterName"),
		ResourceVersion: resourceVersion,
		// KubeRay publishes the workload's own execution window. Using it keeps
		// the reported times independent of when the reconciler happened to poll.
		StartedAt:  parseRayJobTime(status, "startTime"),
		FinishedAt: parseRayJobTime(status, "endTime"),
	}
}

// parseRayJobTime reads an RFC 3339 timestamp from a RayJob status field. An
// absent or unparseable value stays nil so "not started" is never rendered as
// an epoch date.
func parseRayJobTime(status map[string]any, field string) *time.Time {
	raw, ok := status[field]
	if !ok || raw == nil {
		return nil
	}
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(text))
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

// validateRayJobSubmitterRestartPolicy protects the KubeRay contract which
// renders submitterPodTemplate as a batch Job PodTemplateSpec. Kubernetes
// requires its restartPolicy to be Never or OnFailure; the platform requires
// Never so a failed submission is represented by the RayJob rather than being
// retried invisibly by the submitter Job.
func validateRayJobSubmitterRestartPolicy(resource *unstructured.Unstructured) error {
	if resource == nil {
		return fmt.Errorf("RayJob manifest must be provided")
	}
	policy, found, err := unstructured.NestedString(resource.Object, "spec", "submitterPodTemplate", "spec", "restartPolicy")
	if err != nil {
		return fmt.Errorf("read RayJob submitter restart policy: %w", err)
	}
	if !found || policy != "Never" {
		return fmt.Errorf("RayJob submitter pod restartPolicy must be Never")
	}
	return nil
}

func nestedMap(object map[string]any, fields ...string) (map[string]any, bool, error) {
	value, found, err := unstructured.NestedFieldCopy(object, fields...)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("field %s is not an object", strings.Join(fields, "."))
	}
	return result, true, nil
}

func nestedSlice(object map[string]any, fields ...string) ([]any, bool, error) {
	value, found, err := unstructured.NestedFieldCopy(object, fields...)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	result, ok := value.([]any)
	if !ok {
		return nil, false, fmt.Errorf("field %s is not an array", strings.Join(fields, "."))
	}
	return result, true, nil
}

func stringValue(object map[string]any, field string) string {
	value, ok := object[field]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func shellJoin(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value != "" {
		safe := true
		for _, char := range value {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("_./:@%+=,-", char) {
				safe = false
				break
			}
		}
		if safe {
			return value
		}
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isDNSLabel(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			if (index == 0 || index == len(value)-1) && char == '-' {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func isDNSSubdomain(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !isDNSLabel(label) {
			return false
		}
	}
	return true
}

func sanitizeDNS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "default"
	}
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}
