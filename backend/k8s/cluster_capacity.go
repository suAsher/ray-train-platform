package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"ray-train-platform-backend/domain"
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

// Validate rejects malformed capacity without mistaking a successful empty
// node listing for a failed read. Only an entirely zero observation is empty.
// Callers must check the node-list error before using any returned capacity.
func (capacity TrainingPoolCapacity) Validate() error {
	if capacity == (TrainingPoolCapacity{}) {
		return nil
	}
	if capacity.Nodes <= 0 || capacity.GPUs <= 0 || capacity.CPUMillis <= 0 || capacity.MemoryBytes <= 0 ||
		capacity.MaxGPUsPerNode < 0 || capacity.GuaranteedGPUsPerWorker < 0 {
		return fmt.Errorf("invalid training pool capacity: expected positive nodes, GPUs, CPU and memory with nonnegative GPU shape, or an entirely empty observation")
	}
	return nil
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

// TrainingPoolCapacities measures every supported accelerator independently.
// Missing hardware is represented by a zero capacity so stale Kueue quota is
// removed when a pool is drained or decommissioned.
func (c *Client) TrainingPoolCapacities(ctx context.Context, baseSelector map[string]string) (map[domain.AcceleratorClass]TrainingPoolCapacity, error) {
	result := make(map[domain.AcceleratorClass]TrainingPoolCapacity, 4)
	for _, accelerator := range []domain.AcceleratorClass{domain.AcceleratorRTX4090, domain.AcceleratorA100, domain.AcceleratorA800, domain.AcceleratorH20} {
		selector := make(map[string]string, len(baseSelector)+1)
		for key, value := range baseSelector {
			selector[key] = value
		}
		selector["accelerator"] = accelerator.NodeLabelValue()
		capacity, err := c.TrainingPoolCapacity(ctx, selector)
		if err != nil {
			return nil, err
		}
		result[accelerator] = capacity
	}
	return result, nil
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
