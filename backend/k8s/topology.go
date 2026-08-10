package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/domain"
)

type GPUNodeUsage = domain.GPUNodeUsage

func (c *Client) ListGPUNodeUsage(ctx context.Context) ([]GPUNodeUsage, error) {
	if c == nil || c.kubernetes == nil {
		return nil, fmt.Errorf("Kubernetes client is not initialized")
	}
	nodes, err := c.kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list Kubernetes nodes: %w", err)
	}
	pods, err := c.kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase!=Succeeded,status.phase!=Failed"})
	if err != nil {
		return nil, fmt.Errorf("list Kubernetes pods: %w", err)
	}
	allocated := make(map[string]int64)
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if quantity, ok := container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]; ok {
				allocated[pod.Spec.NodeName] += quantity.Value()
			}
		}
	}
	usage := make([]GPUNodeUsage, 0)
	for _, node := range nodes.Items {
		if isVirtualNode(node) {
			continue
		}
		capacityQuantity := node.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")]
		allocatableQuantity := node.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
		capacity := capacityQuantity.Value()
		allocatable := allocatableQuantity.Value()
		if capacity == 0 && allocatable == 0 {
			continue
		}
		used := allocated[node.Name]
		available := allocatable - used
		if available < 0 {
			available = 0
		}
		usage = append(usage, GPUNodeUsage{NodeName: node.Name, Capacity: capacity, Allocatable: allocatable, Allocated: used, Available: available})
	}
	return usage, nil
}

// isVirtualNode reports whether a node is a serverless virtual-kubelet node
// (for example Volcengine VCI). Those nodes publish an elastic GPU quota that
// represents a purchasing limit rather than attached hardware, so including
// them would overstate the cluster's real training capacity.
func isVirtualNode(node corev1.Node) bool {
	if node.Labels["type"] == "virtual-kubelet" {
		return true
	}
	return node.Labels["node.kubernetes.io/instance-type"] == "virtual-node"
}

func (c *Client) ClusterTopology(ctx context.Context) (domain.ClusterTopologyOverview, error) {
	nodes, err := c.ListGPUNodeUsage(ctx)
	if err != nil {
		return domain.ClusterTopologyOverview{}, err
	}
	result := domain.ClusterTopologyOverview{TotalNodes: len(nodes), Nodes: make([]domain.GPUNodeUsage, 0, len(nodes))}
	for _, node := range nodes {
		result.TotalGPUs += int(node.Capacity)
		result.UsedGPUs += int(node.Allocated)
		result.Nodes = append(result.Nodes, node)
	}
	return result, nil
}
