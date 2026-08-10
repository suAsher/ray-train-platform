package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// TrainingPoolCapacity is the schedulable capacity of the GPU training pool.
type TrainingPoolCapacity struct {
	Nodes       int
	GPUs        int64
	CPUMillis   int64
	MemoryBytes int64
}

// TrainingPoolCapacity sums allocatable resources over the Ready nodes that
// carry the training labels. Kueue cannot discover capacity on its own, so the
// platform derives the admission budget from the same labels that decide where
// Ray Pods may run: labelling a new machine is enough to make it usable.
func (c *Client) TrainingPoolCapacity(ctx context.Context, nodeSelector map[string]string) (TrainingPoolCapacity, error) {
	if c == nil || c.kubernetes == nil {
		return TrainingPoolCapacity{}, fmt.Errorf("Kubernetes client is not initialized")
	}
	selector := labels.Set(nodeSelector).AsSelector().String()
	nodes, err := c.kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return TrainingPoolCapacity{}, fmt.Errorf("list training nodes: %w", err)
	}

	capacity := TrainingPoolCapacity{}
	for _, node := range nodes.Items {
		// Virtual-kubelet nodes advertise a purchasing limit rather than
		// hardware; counting them would let Kueue admit work that can never run.
		if isVirtualNode(node) || !isNodeReady(node) {
			continue
		}
		gpu := node.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
		cpu := node.Status.Allocatable[corev1.ResourceCPU]
		memory := node.Status.Allocatable[corev1.ResourceMemory]
		capacity.Nodes++
		capacity.GPUs += gpu.Value()
		capacity.CPUMillis += cpu.MilliValue()
		capacity.MemoryBytes += memory.Value()
	}
	return capacity, nil
}

// isNodeReady treats a node with no reported conditions as ready so that unit
// tests and freshly registered nodes are not silently excluded.
func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return true
}
