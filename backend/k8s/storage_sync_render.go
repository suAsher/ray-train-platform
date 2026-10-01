package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/storagesync"
)

const (
	storageSyncRunLabel = "platform.wellspiking.ai/storage-sync-run"
	storageSyncAttemptLabel = "platform.wellspiking.ai/storage-sync-attempt"
	storageSyncPhaseLabel = "platform.wellspiking.ai/storage-sync-phase"
	storageSyncSpecAnnotation = "platform.wellspiking.ai/storage-sync-spec"
	storageSyncStopAnnotation = "platform.wellspiking.ai/storage-sync-stop-requested"
	storageSyncContainer = "storage-sync"
)

func storageSyncJobName(runID string, attempt int) string {
	digest := sha256.Sum256([]byte(runID))
	return "storage-sync-" + hex.EncodeToString(digest[:12]) + "-" + strconv.Itoa(attempt)
}

func renderStorageSyncJob(cfg config.StorageSyncConfig, spec storagesync.WorkSpec) (*batchv1.Job, *corev1.Secret, error) {
	if !cfg.Enabled { return nil, nil, fmt.Errorf("storage sync is disabled") }
	if err := config.ValidateStorageSyncConfig(cfg); err != nil { return nil, nil, err }
	if spec.RunID == "" || spec.RunID == "." || spec.RunID == ".." || len(k8svalidation.IsValidLabelValue(spec.RunID)) != 0 || spec.Attempt < 1 || spec.Generation < 1 || spec.CallbackURL == "" || spec.CallbackToken == "" {
		return nil, nil, fmt.Errorf("storage sync attempt configuration is incomplete")
	}
	switch spec.Phase {
	case "BROWSE", "PREVIEW", "REVALIDATE", "TRANSFER":
	default: return nil, nil, fmt.Errorf("unsupported storage sync worker phase")
	}
	name := storageSyncJobName(spec.RunID, spec.Attempt)
	labels := map[string]string{storageSyncRunLabel: spec.RunID, storageSyncAttemptLabel: strconv.Itoa(spec.Attempt), storageSyncPhaseLabel: spec.Phase, "app.kubernetes.io/name": storageSyncContainer, "app.kubernetes.io/managed-by": "ray-train-platform"}
	requestSpec := spec
	requestSpec.CallbackToken = ""
	payload, err := json.Marshal(requestSpec)
	if err != nil || len(payload) > 900000 { return nil, nil, fmt.Errorf("storage sync worker request cannot be encoded safely") }
	requestDigest := sha256.Sum256(payload)
	annotations := map[string]string{storageSyncSpecAnnotation: hex.EncodeToString(requestDigest[:])}
	request := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels, Annotations: annotations}, Type: corev1.SecretTypeOpaque, Immutable: pointerTo(true), Data: map[string][]byte{"request.json": payload, "token": []byte(spec.CallbackToken)}}
	volumes, mounts, err := storageSyncVolumes(cfg, spec, name)
	if err != nil { return nil, nil, err }
	args := []string{"--request", "/config/request.json", "--work-dir", "/work/" + spec.RunID, "--callback-token-file", "/var/run/storage-sync/token"}
	if spec.Phase == "TRANSFER" { args = append(args, "--tos-config", "/var/run/raytrain/tosutil/config") }
	nodeSelector := make(map[string]string, len(cfg.NodeSelector))
	for key, value := range cfg.NodeSelector { nodeSelector[key] = value }
	nonRoot := int64(65532)
	container := corev1.Container{
		Name: storageSyncContainer, Image: cfg.Image, ImagePullPolicy: corev1.PullIfNotPresent, Args: args,
		Env: []corev1.EnvVar{
			{Name: "STORAGE_SYNC_POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}},
			{Name: "STORAGE_SYNC_TOS_ENDPOINT", Value: cfg.Endpoint}, {Name: "STORAGE_SYNC_TOS_REGION", Value: cfg.Region},
			{Name: "STORAGE_SYNC_MAX_FILE_CONCURRENCY", Value: strconv.Itoa(cfg.MaxFileConcurrency)},
			{Name: "STORAGE_SYNC_MAX_PART_CONCURRENCY", Value: strconv.Itoa(cfg.MaxPartConcurrency)},
			{Name: "STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND", Value: strconv.FormatInt(cfg.MaxBandwidthBytesPerSecond, 10)},
			{Name: "PYTHONDONTWRITEBYTECODE", Value: "1"}, {Name: "TMPDIR", Value: "/tmp"},
		},
		SecurityContext: &corev1.SecurityContext{RunAsNonRoot: pointerTo(true), RunAsUser: &nonRoot, RunAsGroup: &nonRoot, AllowPrivilegeEscalation: pointerTo(false), ReadOnlyRootFilesystem: pointerTo(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cfg.CPURequest), corev1.ResourceMemory: resource.MustParse(cfg.MemoryRequest), corev1.ResourceEphemeralStorage: resource.MustParse(cfg.EphemeralStorageRequest)},
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cfg.CPULimit), corev1.ResourceMemory: resource.MustParse(cfg.MemoryLimit), corev1.ResourceEphemeralStorage: resource.MustParse(cfg.EphemeralStorageLimit)},
		}, VolumeMounts: mounts,
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels, Annotations: annotations}, Spec: batchv1.JobSpec{
		BackoffLimit: pointerTo(int32(0)), Parallelism: pointerTo(int32(1)), Completions: pointerTo(int32(1)),
		PodReplacementPolicy: pointerTo(batchv1.Failed),
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: pointerTo(false), ServiceAccountName: cfg.ServiceAccountName,
			NodeSelector: nodeSelector, TerminationGracePeriodSeconds: pointerTo(int64(120)),
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: pointerTo(true), RunAsUser: &nonRoot, RunAsGroup: &nonRoot, FSGroup: &nonRoot, FSGroupChangePolicy: pointerTo(corev1.FSGroupChangeOnRootMismatch), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{container}, Volumes: volumes,
		}},
	}}
	return job, request, nil
}

func storageSyncVolumes(cfg config.StorageSyncConfig, spec storagesync.WorkSpec, name string) ([]corev1.Volume, []corev1.VolumeMount, error) {
	volumes := []corev1.Volume{
		{Name: "request", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: pointerTo(int32(0440)), Items: []corev1.KeyToPath{{Key: "request.json", Path: "request.json"}}}}},
		{Name: "callback-token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: pointerTo(int32(0440)), Items: []corev1.KeyToPath{{Key: "token", Path: "token"}}}}},
		{Name: "work", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cfg.WorkClaimName}}},
		{Name: "temporary", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: pointerTo(resource.MustParse(cfg.EphemeralStorageLimit))}}},
	}
	mounts := []corev1.VolumeMount{{Name: "request", MountPath: "/config", ReadOnly: true}, {Name: "callback-token", MountPath: "/var/run/storage-sync", ReadOnly: true}, {Name: "work", MountPath: "/work"}, {Name: "temporary", MountPath: "/tmp"}}
	seen := map[string]string{}
	for _, mapping := range spec.Mappings {
		source := mapping.Source
		if source.Kind != "IDC" { continue }
		if source.SpaceID == "" || len(k8svalidation.IsDNS1123Label(source.SpaceID)) != 0 || source.NFSServer == "" || strings.ContainsAny(source.NFSServer, " /\\\t\r\n") || !strings.HasPrefix(source.NFSRoot, "/") || source.NFSRoot == "/" || path.Clean(source.NFSRoot) != source.NFSRoot {
			return nil, nil, fmt.Errorf("storage sync IDC source is invalid")
		}
		identity := source.NFSServer + "\x00" + source.NFSRoot
		if previous, ok := seen[source.SpaceID]; ok {
			if previous != identity { return nil, nil, fmt.Errorf("storage sync IDC source root changed within the request") }
			continue
		}
		seen[source.SpaceID] = identity
		digest := sha256.Sum256([]byte(source.SpaceID))
		volumeName := "source-" + hex.EncodeToString(digest[:8])
		volumes = append(volumes, corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: source.NFSServer, Path: source.NFSRoot, ReadOnly: true}}})
		mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: "/data/source/" + source.SpaceID, ReadOnly: true})
	}
	if spec.Phase == "TRANSFER" {
		volumes = append(volumes, corev1.Volume{Name: "tos-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: cfg.CredentialSecret, DefaultMode: pointerTo(int32(0440)), Items: []corev1.KeyToPath{{Key: "config", Path: "config"}}}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "tos-config", MountPath: "/var/run/raytrain/tosutil", ReadOnly: true})
	}
	return volumes, mounts, nil
}
