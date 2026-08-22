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
	Nodes                   int
	GPUs                    int64
	MaxGPUsPerNode          int64
	GuaranteedGPUsPerWorker int64
	CPUMillis               int64
	MemoryBytes             int64
}

// TrainingPoolCapacity sums allocatable resources over schedulable, Ready,
// positive-GPU nodes that carry the training labels. Kueue cannot discover
// capacity on its own, so the platform derives the admission budget from the
// same labels that decide where Ray Pods may run: labelling a new machine is
// enough to make it usable.
func (c *Client) TrainingPoolCapacity(ctx context.Context, nodeSelector map[string]string) (TrainingPoolCapacity, error) {
	if c == nil || c.kubernetes == nil {
		return TrainingPoolCapacity{}, fmt.Errorf("Kubernetes client is not initialized")
	}
	effectiveSelector := nodeSelector
	if len(effectiveSelector) == 0 {
		effectiveSelector = defaultTrainingNodeSelector
	}
	selector := labels.Set(effectiveSelector).AsSelector().String()
	nodes, err := c.kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return TrainingPoolCapacity{}, fmt.Errorf("list training nodes: %w", err)
	}

	capacity := TrainingPoolCapacity{}
	for _, node := range nodes.Items {
		// Virtual-kubelet nodes advertise a purchasing limit rather than
		// hardware; counting them would let Kueue admit work that can never run.
		if isVirtualNode(node) || node.Spec.Unschedulable || !isNodeReady(node) {
			continue
		}
		gpu := node.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
		gpuCount := gpu.Value()
		if gpuCount <= 0 {
			continue
		}
		cpu := node.Status.Allocatable[corev1.ResourceCPU]
		memory := node.Status.Allocatable[corev1.ResourceMemory]
		capacity.Nodes++
		capacity.GPUs += gpuCount
		if gpuCount > capacity.MaxGPUsPerNode {
			capacity.MaxGPUsPerNode = gpuCount
		}
		if capacity.GuaranteedGPUsPerWorker == 0 || gpuCount < capacity.GuaranteedGPUsPerWorker {
			capacity.GuaranteedGPUsPerWorker = gpuCount
		}
		capacity.CPUMillis += cpu.MilliValue()
		capacity.MemoryBytes += memory.Value()
	}
	return capacity, nil
}

// isNodeReady only accepts an explicit Ready=True condition. A freshly
// registered node without one is not yet schedulable capacity.
func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
