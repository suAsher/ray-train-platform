package k8s

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

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
	TOSSecretName           string
	TOSEndpoint             string
	TOSBucket               string
	// NodeSelector pins Ray Pods to the GPU training pool. It is configuration
	// so that adding machines or changing GPU model needs no code change.
	NodeSelector map[string]string
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
	if job.Spec.Source.Type == "workspace" && options.IDCExistingClaim == "" {
		return nil, fmt.Errorf("workspace source requires an IDC PVC")
	}
	if job.Spec.Source.Type == "tos" && (options.TOSSecretName == "" || options.TOSEndpoint == "") {
		return nil, fmt.Errorf("tos source requires TOS endpoint and credential Secret")
	}
	if job.Spec.Source.Type == "artifact" {
		if err := validateArtifactMaterialization(job, options); err != nil {
			return nil, err
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
	entrypoint := append(append([]string{}, job.Spec.Entrypoint.Command...), job.Spec.Entrypoint.Args...)

	headPod := podTemplate("ray-head", job.Spec.Image, "4", "16Gi", 0, job.Spec.Source, options, true)
	workerPod := podTemplate("ray-worker", job.Spec.Image, workerCPU, workerMemory, gpusPerWorker, job.Spec.Source, options, false)
	addPodLabels(headPod, job.ID, job.TenantID)
	addPodLabels(workerPod, job.ID, job.TenantID)
	clusterSpec := map[string]any{
		"rayVersion": rayVersion,
		"headGroupSpec": map[string]any{
			"rayStartParams": map[string]any{"dashboard-host": "0.0.0.0", "num-gpus": "0"},
			"template":       headPod,
		},
		"workerGroupSpecs": []any{map[string]any{
			"groupName":      "worker-group",
			"replicas":       workerReplicas,
			"minReplicas":    workerReplicas,
			"maxReplicas":    workerReplicas,
			"rayStartParams": map[string]any{"num-gpus": strconv.FormatInt(gpusPerWorker, 10)},
			"template":       workerPod,
		}},
	}
	labels := map[string]any{
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
			"name":      job.Spec.Name,
			"namespace": namespace,
			"labels":    labels,
			"annotations": map[string]any{
				"ray-train-platform/job-id": job.ID,
				"ray-train-platform/owner":  job.UserID,
			},
		},
		"spec": jobSpecFields(job, clusterSpecField, clusterSpec, entrypoint),
	}
	return &unstructured.Unstructured{Object: jobObject}, nil
}

// defaultCleanupTTLSeconds keeps a finished RayCluster around briefly so the
// Portal can capture the final status before KubeRay removes it.
const defaultCleanupTTLSeconds int64 = 600

func jobSpecFields(job domain.TrainingJob, clusterSpecField string, clusterSpec map[string]any, entrypoint []string) map[string]any {
	cleanupTTL := job.Spec.CleanupPolicy.SuccessTTLSeconds
	if cleanupTTL <= 0 {
		cleanupTTL = defaultCleanupTTLSeconds
	}
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
		"ttlSecondsAfterFinished":  cleanupTTL,
		// Kueue admits a workload by clearing suspend. A job created unsuspended
		// would start immediately and bypass the tenant GPU quota. JobSpec
		// validation guarantees a queue, so this always applies.
		"suspend": true,
	}
	if job.Spec.TimeoutSeconds > 0 {
		spec["activeDeadlineSeconds"] = job.Spec.TimeoutSeconds
	}
	return spec
}

func validateArtifactMaterialization(job domain.TrainingJob, options RenderOptions) error {
	source := job.Spec.Source
	if strings.TrimSpace(source.ArtifactID) == "" || strings.TrimSpace(source.ArtifactObjectKey) == "" || strings.TrimSpace(source.ArtifactSHA256) == "" {
		return fmt.Errorf("artifact source must be materialized before rendering")
	}
	if options.TOSSecretName == "" || options.TOSEndpoint == "" || options.TOSBucket == "" {
		return fmt.Errorf("artifact source requires TOS bucket, endpoint, and credential Secret")
	}
	expectedKey, err := domain.SourceArtifactObjectKey(job.TenantID, job.UserID, source.ArtifactSHA256)
	if err != nil {
		return fmt.Errorf("validate artifact digest: %w", err)
	}
	if source.ArtifactObjectKey != expectedKey {
		return fmt.Errorf("artifact object key is not the immutable owner-scoped key")
	}
	return nil
}

func podTemplate(containerName, image, cpu, memory string, gpus int64, source domain.CodeSource, options RenderOptions, head bool) map[string]any {
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
	if !head {
		env = append(env,
			map[string]any{"name": "NCCL_P2P_DISABLE", "value": "1"},
			map[string]any{"name": "NCCL_IB_DISABLE", "value": "1"},
			map[string]any{"name": "NCCL_DEBUG", "value": "WARN"},
		)
	}
	if options.TOSSecretName != "" {
		for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			env = append(env, map[string]any{
				"name": key,
				"valueFrom": map[string]any{"secretKeyRef": map[string]any{
					"name": options.TOSSecretName,
					"key":  key,
				}},
			})
		}
	}
	if options.TOSEndpoint != "" {
		env = append(env, map[string]any{"name": "TOS_ENDPOINT", "value": options.TOSEndpoint})
	}
	if options.TOSBucket != "" {
		env = append(env, map[string]any{"name": "TOS_BUCKET", "value": options.TOSBucket})
	}
	volumeMounts := []any{
		map[string]any{"name": "workspace", "mountPath": "/workspace"},
		map[string]any{"name": "dshm", "mountPath": "/dev/shm"},
		map[string]any{"name": "ray-spill", "mountPath": "/tmp/ray-spill"},
	}
	volumes := []any{
		map[string]any{"name": "workspace", "emptyDir": map[string]any{}},
		map[string]any{"name": "dshm", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "32Gi"}},
		map[string]any{"name": "ray-spill", "emptyDir": map[string]any{}},
	}
	if options.IDCExistingClaim != "" {
		mountPath := options.IDCMountPath
		if mountPath == "" {
			mountPath = "/mnt/idc"
		}
		volumeMounts = append(volumeMounts, map[string]any{"name": "idc-storage", "mountPath": mountPath, "readOnly": false})
		volumes = append(volumes, map[string]any{
			"name":                  "idc-storage",
			"persistentVolumeClaim": map[string]any{"claimName": options.IDCExistingClaim, "readOnly": false},
		})
	}
	podSpec := map[string]any{
		"serviceAccountName":           options.ServiceAccount,
		"automountServiceAccountToken": options.ServiceAccount != "",
		"securityContext":              map[string]any{"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers": []any{map[string]any{
			"name":            containerName,
			"image":           image,
			"imagePullPolicy": "IfNotPresent",
			"workingDir":      "/workspace",
			"resources":       resources,
			"env":             env,
			"volumeMounts":    volumeMounts,
			"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
		}},
		"volumes":        volumes,
		"initContainers": []any{sourceMaterializer(source, options)},
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

func renderImagePullSecrets(names []string) []any {
	pullSecrets := make([]any, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			pullSecrets = append(pullSecrets, map[string]any{"name": strings.TrimSpace(name)})
		}
	}
	return pullSecrets
}

func sourceMaterializer(source domain.CodeSource, options RenderOptions) map[string]any {
	if source.Type == "artifact" {
		return artifactSourceMaterializer(source, options)
	}
	command := "set -eu\nfind /workspace -mindepth 1 -maxdepth 1 -exec rm -rf {} +\n"
	idcMountPath := options.IDCMountPath
	if idcMountPath == "" {
		idcMountPath = "/mnt/idc"
	}
	switch source.Type {
	case "git":
		// The workspace volume is created by the kubelet and is not owned by the
		// container user, so git aborts with "detected dubious ownership".
		// safe.directory is passed per command rather than written with
		// --global because the materializer image has no writable HOME.
		git := "git -c safe.directory=/workspace"
		command += git + " init /workspace\n"
		command += git + " -C /workspace remote add origin " + shellQuote(source.URL) + "\n"
		command += git + " -C /workspace fetch --depth 1 origin " + shellQuote(source.Commit) + "\n"
		command += git + " -C /workspace checkout --detach FETCH_HEAD\n"
	case "tos":
		parsed, _ := url.Parse(source.URI)
		objectURI := "s3://" + parsed.Host + strings.TrimRight(parsed.Path, "/")
		command += "mkdir -p /tmp/platform-source\n"
		command += "aws s3 cp --no-progress --endpoint-url \"$TOS_ENDPOINT\" " + shellQuote(objectURI) + " /tmp/platform-source/code-artifact\n"
		command += "/usr/local/bin/safe-extract --archive /tmp/platform-source/code-artifact --destination /workspace\n"
	case "workspace":
		command += "cp -a " + shellQuote(idcMountPath+"/snapshots/"+source.Snapshot) + "/. /workspace/\n"
	}
	env := []any{}
	if source.Type == "tos" {
		env = append(env,
			map[string]any{"name": "TOS_ENDPOINT", "value": options.TOSEndpoint},
			map[string]any{"name": "AWS_ACCESS_KEY_ID", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": options.TOSSecretName, "key": "AWS_ACCESS_KEY_ID"}}},
			map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": options.TOSSecretName, "key": "AWS_SECRET_ACCESS_KEY"}}},
		)
	}
	volumeMounts := []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}
	if source.Type == "workspace" {
		volumeMounts = append(volumeMounts, map[string]any{"name": "idc-storage", "mountPath": idcMountPath, "readOnly": true})
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
		"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
		"volumeMounts":    volumeMounts,
	}
}

func artifactSourceMaterializer(source domain.CodeSource, options RenderOptions) map[string]any {
	env := []any{
		map[string]any{"name": "TOS_ENDPOINT", "value": options.TOSEndpoint},
		map[string]any{"name": "TOS_BUCKET", "value": options.TOSBucket},
		map[string]any{"name": "AWS_ACCESS_KEY_ID", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": options.TOSSecretName, "key": "AWS_ACCESS_KEY_ID"}}},
		map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": options.TOSSecretName, "key": "AWS_SECRET_ACCESS_KEY"}}},
	}
	return map[string]any{
		"name":            "source-materializer",
		"image":           options.SourceMaterializerImage,
		"imagePullPolicy": "IfNotPresent",
		"command":         []any{"/usr/local/bin/safe-extract"},
		"args": []any{
			"tos", "--endpoint", options.TOSEndpoint, "--bucket", options.TOSBucket,
			"--object-key", source.ArtifactObjectKey, "--sha256", source.ArtifactSHA256, "--destination", "/workspace",
		},
		"env": env,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
		},
		"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
		"volumeMounts":    []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}},
	}
}

func addPodLabels(template map[string]any, jobID, tenantID string) {
	template["metadata"] = map[string]any{"labels": map[string]any{"platform_job_id": jobID, "platform_tenant_id": tenantID}}
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
	case "PROVISIONING", "DEPLOYING":
		state = domain.StateProvisioning
	default:
		state = domain.StateUnknown
	}
	reason := stringValue(status, "reason")
	message := stringValue(status, "message")
	if message == "" {
		message = stringValue(status, "jobStatus")
	}
	return domain.ObservedJobState{
		ID:              jobID,
		State:           state,
		Reason:          reason,
		Message:         message,
		RayJobUID:       stringValue(status, "rayJobUID"),
		RayClusterName:  stringValue(status, "rayClusterName"),
		ResourceVersion: resourceVersion,
	}
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
