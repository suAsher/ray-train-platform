package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func gpuNode(name string, labels map[string]string, gpus string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(gpus)},
			Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(gpus)},
		},
	}
}

// Volcengine VCI virtual-kubelet nodes advertise a large elastic GPU quota that
// is a purchasing limit, not physical hardware. Counting it makes the capacity
// dashboard report thousands of cards that cannot actually run a training job.
func TestListGPUNodeUsageExcludesVirtualKubeletNodes(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		gpuNode("real-1", map[string]string{"accelerator": "nvidia-rtx-4090"}, "8"),
		gpuNode("vci-node1-cn-shanghai-a", map[string]string{"type": "virtual-kubelet"}, "1000"),
		gpuNode("vci-node1-cn-shanghai-b", map[string]string{"node.kubernetes.io/instance-type": "virtual-node"}, "1000"),
	)}

	items, err := client.ListGPUNodeUsage(context.Background())
	if err != nil {
		t.Fatalf("list GPU usage: %v", err)
	}
	if len(items) != 1 || items[0].NodeName != "real-1" || items[0].Capacity != 8 {
		t.Fatalf("expected only the physical GPU node, got %+v", items)
	}
}

func TestClusterTopologyReportsOnlyPhysicalGPUs(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		gpuNode("real-1", map[string]string{"accelerator": "nvidia-rtx-4090"}, "8"),
		gpuNode("real-2", map[string]string{"accelerator": "nvidia-rtx-4090"}, "8"),
		gpuNode("vci-node1-cn-shanghai-a", map[string]string{"type": "virtual-kubelet"}, "1000"),
	)}

	overview, err := client.ClusterTopology(context.Background())
	if err != nil {
		t.Fatalf("cluster topology: %v", err)
	}
	if overview.TotalGPUs != 16 || overview.TotalNodes != 2 {
		t.Fatalf("expected 16 GPUs over 2 nodes, got %d over %d", overview.TotalGPUs, overview.TotalNodes)
	}
}
