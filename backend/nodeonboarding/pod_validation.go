package nodeonboarding

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
)

// matchPodSpec compares the entire fixed template after normalizing only known
// API/scheduler defaults. Unexpected commands, mounts, sidecars or privileges fail.
func matchPodSpec(actual, desired *corev1.Pod, node *corev1.Node) error {
	if actual.Spec.NodeName != "" && actual.Spec.NodeName != node.Name {
		return fmt.Errorf("probe scheduled to unexpected node")
	}
	a, d := actual.Spec.DeepCopy(), desired.Spec.DeepCopy()
	// VKE injects one ENI only into the fixed scheduled probe. Strip only the
	// observed symmetric pair; all other resources remain part of strict equality.
	if actual.Name == resourceName(node, "probe") && desired.Name == actual.Name && len(a.Containers) == 1 && len(d.Containers) == 1 && a.Containers[0].Name == "check" && d.Containers[0].Name == "check" {
		const eni corev1.ResourceName = "vke.volcengine.com/eni-ip"
		request, hasRequest := a.Containers[0].Resources.Requests[eni]
		limit, hasLimit := a.Containers[0].Resources.Limits[eni]
		one := resource.MustParse("1")
		if hasRequest && hasLimit && request.Cmp(one) == 0 && limit.Cmp(one) == 0 {
			delete(a.Containers[0].Resources.Requests, eni)
			delete(a.Containers[0].Resources.Limits, eni)
		}
	}
	if d.NodeName == "" {
		a.NodeName = ""
	}
	normalizePodDefaults(a)
	normalizePodDefaults(d)
	if !equality.Semantic.DeepEqual(a, d) {
		return fmt.Errorf("owned Pod %s differs from fixed template", actual.Name)
	}
	return nil
}

func normalizePodDefaults(spec *corev1.PodSpec) {
	if spec.DNSPolicy == "" {
		spec.DNSPolicy = corev1.DNSClusterFirst
	}
	if spec.SchedulerName == "" {
		spec.SchedulerName = corev1.DefaultSchedulerName
	}
	if spec.EnableServiceLinks == nil {
		spec.EnableServiceLinks = pointer(true)
	}
	if spec.PreemptionPolicy == nil {
		spec.PreemptionPolicy = pointer(corev1.PreemptLowerPriority)
	}
	if spec.Priority == nil {
		if spec.PriorityClassName == "node-onboarding-probe" {
			spec.Priority = pointer(int32(-1000))
		} else {
			spec.Priority = pointer(int32(0))
		}
	}
	if spec.DeprecatedServiceAccount == "" {
		spec.DeprecatedServiceAccount = spec.ServiceAccountName
	}
	var tolerations []corev1.Toleration
	for _, t := range spec.Tolerations {
		standard := (t.Key == "node.kubernetes.io/not-ready" || t.Key == "node.kubernetes.io/unreachable") && t.Operator == corev1.TolerationOpExists && t.Value == "" && t.Effect == corev1.TaintEffectNoExecute && t.TolerationSeconds != nil && *t.TolerationSeconds == 300
		if !standard {
			tolerations = append(tolerations, t)
		}
	}
	spec.Tolerations = tolerations
	for i := range spec.Containers {
		container := &spec.Containers[i]
		if container.TerminationMessagePath == "" {
			container.TerminationMessagePath = corev1.TerminationMessagePathDefault
		}
		if container.TerminationMessagePolicy == "" {
			container.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		}
	}
}
