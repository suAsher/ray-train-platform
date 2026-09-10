package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/idcsync"
)

const (
	idcSyncManagedLabel = "platform.wellspiking.ai/idc-sync-run"
	idcSyncContainer    = "idc-sync"
)

func (c *Client) EnsureIDCSyncJob(ctx context.Context, spec idcsync.JobSpec) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("IDC sync Kubernetes client is not initialized")
	}
	desired, err := renderIDCSyncJob(spec)
	if err != nil {
		return err
	}
	jobs := c.kubernetes.BatchV1().Jobs(desired.Namespace)
	existing, err := jobs.Get(ctx, desired.Name, metav1.GetOptions{})
	if err == nil {
		if existing.Labels[idcSyncManagedLabel] != desired.Labels[idcSyncManagedLabel] || existing.Spec.Template.Spec.Containers[0].Image != desired.Spec.Template.Spec.Containers[0].Image {
			return fmt.Errorf("IDC sync Job name is already owned by another workload")
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get IDC sync Job: %w", err)
	}
	if _, err := jobs.Create(ctx, desired, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("validate IDC sync Job: %w", err)
	}
	if _, err := jobs.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create IDC sync Job: %w", err)
	}
	return nil
}

func renderIDCSyncJob(spec idcsync.JobSpec) (*batchv1.Job, error) {
	if strings.TrimSpace(spec.RunID) == "" || strings.TrimSpace(spec.Namespace) == "" || strings.TrimSpace(spec.Image) == "" || strings.TrimSpace(spec.CallbackURL) == "" || strings.TrimSpace(spec.CallbackToken) == "" {
		return nil, fmt.Errorf("IDC sync Job has incomplete platform configuration")
	}
	if !strings.HasPrefix(spec.SourceNFSPath, "/") || path.Clean(spec.SourceNFSPath) != spec.SourceNFSPath || spec.SourceNFSPath == "/" || strings.ContainsAny(spec.SourceNFSServer, " /\\\t") {
		return nil, fmt.Errorf("IDC sync source is invalid")
	}
	hash := sha256.Sum256([]byte(spec.RunID))
	name := "idc-sync-" + hex.EncodeToString(hash[:8])
	labels := map[string]string{idcSyncManagedLabel: spec.RunID, "app.kubernetes.io/managed-by": "ray-train-platform", "app.kubernetes.io/name": "idc-sync"}
	nonRoot := int64(65532)
	backoff := int32(2)
	args := []string{"--run-id", spec.RunID, "--source-relative-path", spec.SourceRelativePath, "--mirror-prefix", spec.MirrorPrefix, "--internal-prefix", spec.InternalPrefix, "--bucket", spec.Bucket, "--tosutil-config", "/var/run/raytrain/tosutil/config", "--callback-url", spec.CallbackURL}
	if spec.PreviousInventoryKey != "" {
		args = append(args, "--previous-inventory-key", spec.PreviousInventoryKey)
	}
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: spec.Namespace, Labels: labels}, Spec: batchv1.JobSpec{BackoffLimit: &backoff, TTLSecondsAfterFinished: pointerTo(int32(86400)), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: spec.ServiceAccountName, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: pointerTo(true), RunAsUser: &nonRoot, RunAsGroup: &nonRoot, FSGroup: &nonRoot}, Containers: []corev1.Container{{Name: idcSyncContainer, Image: spec.Image, ImagePullPolicy: corev1.PullIfNotPresent, Args: args, Env: []corev1.EnvVar{{Name: "IDC_SYNC_CALLBACK_TOKEN", Value: spec.CallbackToken}}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: pointerTo(false), ReadOnlyRootFilesystem: pointerTo(true), RunAsNonRoot: pointerTo(true), RunAsUser: &nonRoot, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{}, VolumeMounts: []corev1.VolumeMount{{Name: "source", MountPath: "/data/source", ReadOnly: true}, {Name: "work", MountPath: "/work"}, {Name: "tosutil-config", MountPath: "/var/run/raytrain/tosutil", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "source", VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: spec.SourceNFSServer, Path: spec.SourceNFSPath, ReadOnly: true}}}, {Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "tosutil-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: spec.TosutilConfigSecret, Items: []corev1.KeyToPath{{Key: "config", Path: "config"}}}}}}}}}}, nil
}

func pointerTo[T any](value T) *T { return &value }
