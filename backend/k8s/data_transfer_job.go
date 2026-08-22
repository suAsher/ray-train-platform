package k8s

import (
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/domain"
)

// DataTransferJobOptions are all deployment-owned controls. In particular,
// SFTPHost and KnownHostsConfigMap are never populated from a browser request.
type DataTransferJobOptions struct {
	Namespace           string
	Image               string
	ServiceAccount      string
	KnownHostsConfigMap string
	SFTPHost            string
	SFTPPort            int32
	NodeSelector        map[string]string
}

// RenderDataTransferJob creates the short-lived Job that copies only between
// a caller's personal SFTP account and their personal TOS claim. It has no
// cloud credential, host-path mount, or shell interpolation of user paths.
func RenderDataTransferJob(transfer domain.DataTransfer, connection domain.PersonalIDCConnection, personalClaimName string, options DataTransferJobOptions) (*batchv1.Job, error) {
	if err := transfer.Validate(); err != nil {
		return nil, err
	}
	if err := connection.Validate(); err != nil {
		return nil, err
	}
	if connection.State != domain.IDCConnectionReady {
		return nil, fmt.Errorf("IDC connection is not verified")
	}
	if connection.TenantID != transfer.TenantID || connection.UserID != transfer.UserID {
		return nil, fmt.Errorf("IDC connection does not belong to data transfer owner")
	}
	if strings.TrimSpace(options.Namespace) == "" || strings.TrimSpace(personalClaimName) == "" {
		return nil, fmt.Errorf("data transfer namespace and personal claim are required")
	}
	if err := domain.ValidatePinnedImage(options.Image); err != nil {
		return nil, fmt.Errorf("data mover image: %w", err)
	}
	if err := validateDataTransferSFTPOptions(options); err != nil {
		return nil, err
	}

	activeDeadlineSeconds := int64(12 * 60 * 60)
	ttlSecondsAfterFinished := int32(24 * 60 * 60)
	backoffLimit := int32(0)
	falseValue := false
	trueValue := true
	userID := int64(65532)
	defaultMode := int32(0o400)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataTransferJobName(transfer.ID),
			Namespace: options.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":        "ray-train-platform",
				"app.kubernetes.io/name":              "ray-data-mover",
				"platform.wellspiking.ai/transfer-id": transfer.ID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadlineSeconds,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				ServiceAccountName:           options.ServiceAccount,
				AutomountServiceAccountToken: &falseValue,
				RestartPolicy:                corev1.RestartPolicyNever,
				NodeSelector:                 cloneDataTransferNodeSelector(options.NodeSelector),
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: &trueValue,
					RunAsUser:    &userID,
					FSGroup:      &userID,
				},
				InitContainers: []corev1.Container{{
					Name:    "prepare-idc-key",
					Image:   options.Image,
					Command: []string{"/bin/sh", "-ec"},
					Args:    []string{"install -m 0400 -o 65532 -g 65532 /secret/id_ed25519 /prepared-key/id_ed25519"},
					SecurityContext: &corev1.SecurityContext{
						RunAsUser:                int64Pointer(0),
						AllowPrivilegeEscalation: &falseValue,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "idc-key", MountPath: "/secret", ReadOnly: true},
						{Name: "prepared-key", MountPath: "/prepared-key"},
					},
				}},
				Containers: []corev1.Container{{
					Name:    "data-mover",
					Image:   options.Image,
					Command: []string{"/app/entrypoint.sh"},
					Args:    []string{string(transfer.Direction), transfer.IDCRelativePath, transfer.TOSLocation.RelativePath},
					Env: []corev1.EnvVar{
						{Name: "IDC_SFTP_HOST", Value: options.SFTPHost},
						{Name: "IDC_SFTP_PORT", Value: fmt.Sprintf("%d", options.SFTPPort)},
						{Name: "IDC_SFTP_USERNAME", Value: connection.RemoteUsername},
						{Name: "IDC_SFTP_KEY_FILE", Value: "/var/run/idc-key/id_ed25519"},
						{Name: "IDC_SFTP_KNOWN_HOSTS_FILE", Value: "/etc/idc-known-hosts/known_hosts"},
					},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					}, Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					}},
					SecurityContext: &corev1.SecurityContext{
						RunAsNonRoot:             &trueValue,
						RunAsUser:                &userID,
						AllowPrivilegeEscalation: &falseValue,
						ReadOnlyRootFilesystem:   &trueValue,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "personal-tos", MountPath: "/tos"},
						{Name: "prepared-key", MountPath: "/var/run/idc-key", ReadOnly: true},
						{Name: "known-hosts", MountPath: "/etc/idc-known-hosts", ReadOnly: true},
					},
				}},
				Volumes: []corev1.Volume{
					{Name: "personal-tos", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: personalClaimName}}},
					{Name: "idc-key", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
						SecretName: connection.SecretName, DefaultMode: &defaultMode,
						Items: []corev1.KeyToPath{{Key: "id_ed25519", Path: "id_ed25519"}},
					}}},
					{Name: "prepared-key", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					{Name: "known-hosts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: options.KnownHostsConfigMap},
						Items:                []corev1.KeyToPath{{Key: "known_hosts", Path: "known_hosts"}},
					}}},
				},
			}},
		},
	}, nil
}

func validateDataTransferSFTPOptions(options DataTransferJobOptions) error {
	if strings.TrimSpace(options.ServiceAccount) == "" || strings.TrimSpace(options.KnownHostsConfigMap) == "" {
		return fmt.Errorf("data mover service account and known-hosts ConfigMap are required")
	}
	host := strings.TrimSpace(options.SFTPHost)
	if host == "" || host != options.SFTPHost || strings.ContainsAny(host, "/\\@:") {
		return fmt.Errorf("SFTP host must be a configured hostname without a path or user")
	}
	if options.SFTPPort < 1 || options.SFTPPort > 65535 {
		return fmt.Errorf("SFTP port must be between 1 and 65535")
	}
	return nil
}

func dataTransferJobName(id string) string {
	name := strings.ToLower(id)
	name = strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			return char
		}
		return '-'
	}, name)
	name = strings.Trim(name, "-")
	if len(name) > 42 {
		name = strings.TrimRight(name[:42], "-")
	}
	return "data-transfer-" + name
}

func cloneDataTransferNodeSelector(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func int64Pointer(value int64) *int64 { return &value }
