package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"ray-train-platform-backend/domain"
)

type TeamWorkerNodePreferenceRequest struct {
	TenantID         string
	JobID            string
	AcceleratorClass domain.AcceleratorClass
}

func (c *Client) PreferredTeamWorkerNodes(ctx context.Context, request TeamWorkerNodePreferenceRequest) ([]string, error) {
	if c == nil || c.kubernetes == nil {
		return nil, fmt.Errorf("Kubernetes client is not initialized")
	}
	tenantID := strings.TrimSpace(request.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant id is required")
	}
	selector := labels.SelectorFromSet(labels.Set{
		"platform_tenant_id": tenantID,
		"ray.io/node-type":   "worker",
	}).String()
	pods, err := c.kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list tenant worker pods: %w", err)
	}
	acceleratorLabel := request.AcceleratorClass.Resolved().NodeLabelValue()
	currentJobID := strings.TrimSpace(request.JobID)
	allocatedByNode := map[string]int64{}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" || pod.Labels[platformJobIDLabel] == currentJobID {
			continue
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			continue
		}
		if strings.TrimSpace(pod.Spec.NodeSelector["accelerator"]) != acceleratorLabel {
			continue
		}
		requestedGPU := podGPURequests(pod)
		if requestedGPU <= 0 {
			continue
		}
		allocatedByNode[pod.Spec.NodeName] += requestedGPU
	}
	nodes := make([]string, 0, len(allocatedByNode))
	for node := range allocatedByNode {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		left, right := allocatedByNode[nodes[i]], allocatedByNode[nodes[j]]
		if left != right {
			return left > right
		}
		return nodes[i] < nodes[j]
	})
	return nodes, nil
}

func podGPURequests(pod corev1.Pod) int64 {
	var requested int64
	for _, container := range pod.Spec.Containers {
		if quantity, found := container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]; found {
			requested += quantity.Value()
		}
	}
	return requested
}
