package k8s

import corev1 "k8s.io/api/core/v1"

// SchedulingPodRequests follows scheduler init-sidecar semantics: restartable
// init containers run alongside later init containers and the application.
// Pod overhead also occupies capacity. The returned map never aliases the Pod.
func SchedulingPodRequests(pod corev1.Pod) corev1.ResourceList {
	requests := corev1.ResourceList{}
	for _, container := range pod.Spec.Containers {
		addCapacityResources(requests, container.Resources.Requests)
	}
	restartable, initPeak := corev1.ResourceList{}, corev1.ResourceList{}
	for _, container := range pod.Spec.InitContainers {
		current := corev1.ResourceList{}
		addCapacityResources(current, restartable)
		addCapacityResources(current, container.Resources.Requests)
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			addCapacityResources(restartable, container.Resources.Requests)
			addCapacityResources(requests, container.Resources.Requests)
		}
		maxCapacityResources(initPeak, current)
	}
	maxCapacityResources(requests, initPeak)
	addCapacityResources(requests, pod.Spec.Overhead)
	return requests
}

func addCapacityResources(target, source corev1.ResourceList) {
	for key, quantity := range source {
		value := target[key]
		value.Add(quantity)
		target[key] = value
	}
}

func maxCapacityResources(target, source corev1.ResourceList) {
	for key, quantity := range source {
		value := target[key]
		if quantity.Cmp(value) > 0 {
			target[key] = quantity.DeepCopy()
		}
	}
}

func capacityResourceValue(resources corev1.ResourceList, name corev1.ResourceName) int64 {
	q := resources[name]
	if name == corev1.ResourceCPU {
		return q.MilliValue()
	}
	return q.Value()
}
