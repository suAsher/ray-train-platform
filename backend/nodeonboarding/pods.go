package nodeonboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type nfsShare struct {
	Server string `json:"server"`
	Path   string `json:"path"`
}

func (c *Controller) nfsShares(ctx context.Context) ([]nfsShare, error) {
	cm, err := c.client.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, c.config.NFSConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var shares []nfsShare
	decoder := json.NewDecoder(strings.NewReader(cm.Data["shares.json"]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&shares); err != nil {
		return nil, fmt.Errorf("invalid NFS shares.json: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing NFS JSON")
	}
	if len(shares) < 1 || len(shares) > 8 {
		return nil, fmt.Errorf("NFS config requires 1..8 shares")
	}
	for _, share := range shares {
		if net.ParseIP(share.Server) == nil && !validNode(share.Server) {
			return nil, fmt.Errorf("invalid NFS server")
		}
		if share.Path == "/" || !strings.HasPrefix(share.Path, "/") || path.Clean(share.Path) != share.Path || strings.ContainsAny(share.Path, "\n\r\t\\") {
			return nil, fmt.Errorf("invalid NFS path")
		}
	}
	return shares, nil
}
func pointer[T any](value T) *T { return &value }
func (c *Controller) basePod(node *corev1.Node, suffix, image string, uid int64) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: objectMeta(node, c.config.Namespace, suffix), Spec: corev1.PodSpec{AutomountServiceAccountToken: pointer(false), RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: pointer(int64(300)), TerminationGracePeriodSeconds: pointer(int64(10)), SecurityContext: &corev1.PodSecurityContext{RunAsUser: &uid, RunAsGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "check", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: pointer(false), ReadOnlyRootFilesystem: pointer(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}}}}
}
func (c *Controller) preparePod(node *corev1.Node) *corev1.Pod {
	pod := c.basePod(node, "prepare", c.config.Image, 0)
	pod.Spec.PreemptionPolicy = pointer(corev1.PreemptNever)
	pod.Spec.PriorityClassName = "node-onboarding-probe"
	pod.Spec.ServiceAccountName = c.config.ProbeServiceAccount
	pod.Spec.ActiveDeadlineSeconds = pointer(int64(600))
	warm := c.basePod(node, "warm", c.config.HelperImage, 1000).Spec.Containers[0]
	warm.Name = "warm-helper"
	warm.Command = []string{"/bin/sh", "-ec", "true"}
	warm.SecurityContext.RunAsUser = pointer(int64(1000))
	warm.SecurityContext.RunAsGroup = pointer(int64(1000))
	warm.SecurityContext.RunAsNonRoot = pointer(true)
	pod.Spec.Containers = append(pod.Spec.Containers, warm)
	pod.Spec.NodeName = node.Name
	pod.Spec.Containers[0].Command = []string{"/app/node-onboarding", "prepare"}
	for _, target := range []string{"/data1", "/data2"} {
		name := strings.TrimPrefix(target, "/")
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: target, Type: pointer(corev1.HostPathDirectory)}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: name, MountPath: target})
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "mountinfo", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/proc/1/mountinfo", Type: pointer(corev1.HostPathFile)}}})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "mountinfo", MountPath: "/host-mountinfo", ReadOnly: true})
	for _, metadata := range []struct{ name, path string }{{"sys-dev-block", "/sys/dev/block"}, {"sys-devices", "/sys/devices"}} {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: metadata.name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: metadata.path, Type: pointer(corev1.HostPathDirectory)}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: metadata.name, MountPath: "/host-" + strings.TrimPrefix(metadata.path, "/"), ReadOnly: true})
	}
	return pod
}
func (c *Controller) probePod(node *corev1.Node, shares []nfsShare) *corev1.Pod {
	pod := c.basePod(node, "probe", c.config.HelperImage, 1000)
	pod.Spec.PreemptionPolicy = pointer(corev1.PreemptNever)
	pod.Spec.PriorityClassName = "node-onboarding-probe"
	pod.Spec.ServiceAccountName = c.config.ProbeServiceAccount
	pod.Spec.ActiveDeadlineSeconds = pointer(int64(600))
	pod.Spec.SecurityContext.RunAsNonRoot = pointer(true)
	pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": node.Labels["kubernetes.io/hostname"]}
	pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node.Name}}}}}}}}
	script := "set -eu; umask 077; for d in /cache1 /cache2; do printf '%s' onboarding-probe > \"$d/marker\"; test \"$(cat \"$d/marker\")\" = onboarding-probe; rm \"$d/marker\"; done"
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("cache%d", i)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: resourceName(node, name)}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: name, MountPath: "/" + name})
	}
	for i, share := range shares {
		name := fmt.Sprintf("nfs-%d", i)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: share.Server, Path: share.Path, ReadOnly: true}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: name, MountPath: "/" + name, ReadOnly: true})
		script += "; test -r /" + name + "/."
	}
	pod.Spec.Containers[0].Command = []string{"/bin/sh", "-ec", script}
	return pod
}
