package k8s

import (
	"context"
	"encoding/json"
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
		item := GPUNodeUsage{
			NodeName: node.Name, Capacity: capacity, Allocatable: allocatable, Allocated: used, Available: available,
			AcceleratorClass: acceleratorClassFromNodeLabel(node.Labels["accelerator"]),
			GPUPool:          node.Labels["platform.wellspiking.ai/gpu-pool"],
			AssignedTenant:   node.Labels["platform.wellspiking.ai/tenant"],
		}
		usage = append(usage, withNodeOnboarding(item, node))
	}
	return usage, nil
}

func acceleratorClassFromNodeLabel(value string) domain.AcceleratorClass {
	switch value {
	case "nvidia-a100":
		return domain.AcceleratorA100
	case "nvidia-a800":
		return domain.AcceleratorA800
	case "nvidia-h20":
		return domain.AcceleratorH20
	case "nvidia-rtx-4090":
		return domain.AcceleratorRTX4090
	default:
		return ""
	}
}

func withNodeOnboarding(item GPUNodeUsage, node corev1.Node) GPUNodeUsage {
	ready, cordoned := false, node.Spec.Unschedulable
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			ready = condition.Status == corev1.ConditionTrue
		}
	}
	item.NodeReady, item.Cordoned = &ready, &cordoned
	raw := node.Annotations["platform.wellspiking.ai/onboarding-state"]
	var state struct {
		UID   string `json:"uid"`
		Stage string `json:"stage"`
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &state) != nil || state.UID == "" || state.UID != string(node.UID) {
		return item
	}
	switch state.Stage {
	case "prepare", "probe", "cleanup", "ready":
	default:
		return item
	}
	cacheReady := state.Stage == "ready" && node.Labels["platform.wellspiking.ai/cache-ready"] == "true"
	item.CacheReady, item.OnboardingStage = &cacheReady, state.Stage
	reason := []rune(node.Annotations["platform.wellspiking.ai/onboarding-reason"])
	if len(reason) > 400 {
		reason = reason[:400]
	}
	item.OnboardingReason = string(reason)
	return item
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
