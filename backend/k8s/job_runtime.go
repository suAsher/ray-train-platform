package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/domain"
)

const platformJobIDLabel = "platform_job_id"

func (c *Client) ListJobRuntime(ctx context.Context, namespace, jobID string) (domain.JobRuntime, error) {
	if c == nil || c.kubernetes == nil {
		return domain.JobRuntime{}, fmt.Errorf("Kubernetes client is not initialized")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(jobID) == "" {
		return domain.JobRuntime{}, fmt.Errorf("job namespace and id are required")
	}

	pods, err := c.kubernetes.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: platformJobIDLabel + "=" + jobID,
	})
	if err != nil {
		return domain.JobRuntime{}, fmt.Errorf("list job pods: %w", err)
	}

	runtime := domain.JobRuntime{
		JobID: jobID, Namespace: namespace, Pods: make([]domain.JobPodRuntime, 0, len(pods.Items)),
	}
	for _, pod := range pods.Items {
		runtime.Pods = append(runtime.Pods, toJobPodRuntime(pod))
	}
	sort.Slice(runtime.Pods, func(i, j int) bool {
		return runtime.Pods[i].Name < runtime.Pods[j].Name
	})
	return runtime, nil
}

func toJobPodRuntime(pod corev1.Pod) domain.JobPodRuntime {
	phase := string(pod.Status.Phase)
	if phase == "" {
		phase = string(corev1.PodPending)
	}
	requestedGPU := int64(0)
	for _, container := range pod.Spec.Containers {
		if quantity, found := container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]; found {
			requestedGPU += quantity.Value()
		}
	}
	return domain.JobPodRuntime{
		Name: pod.Name, Role: podRuntimeRole(pod), Phase: phase,
		Reason: pod.Status.Reason, Message: pod.Status.Message,
		NodeName: pod.Spec.NodeName, PodIP: pod.Status.PodIP, GPURequested: requestedGPU,
	}
}

func podRuntimeRole(pod corev1.Pod) string {
	if role := strings.TrimSpace(pod.Labels["ray.io/node-type"]); role != "" {
		return role
	}
	if strings.Contains(pod.Name, "submitter") {
		return "submitter"
	}
	return "pod"
}
