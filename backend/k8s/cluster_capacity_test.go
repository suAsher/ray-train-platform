package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func trainingNode(name string, labels map[string]string, gpus, cpu, memory string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				"nvidia.com/gpu":      resource.MustParse(gpus),
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
			},
		},
	}
}

var trainingPool = map[string]string{"accelerator": "nvidia-rtx-4090"}

// Adding a labelled machine has to be enough: the platform derives the Kueue
// budget from the nodes that actually carry the training label.
func TestTrainingPoolCapacitySumsMatchingNodes(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		trainingNode("gpu-1", trainingPool, "8", "64", "512Gi"),
		trainingNode("gpu-2", trainingPool, "8", "64", "512Gi"),
		trainingNode("gpu-3", trainingPool, "8", "64", "512Gi"),
	)}

	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.GPUs != 24 {
		t.Fatalf("expected 24 GPUs across the pool, got %d", capacity.GPUs)
	}
	if capacity.CPUMillis != 192_000 {
		t.Fatalf("expected 192 cores, got %d millis", capacity.CPUMillis)
	}
	if capacity.MemoryBytes != 3*512*1024*1024*1024 {
		t.Fatalf("unexpected memory total: %d", capacity.MemoryBytes)
	}
	if capacity.Nodes != 3 {
		t.Fatalf("expected 3 nodes, got %d", capacity.Nodes)
	}
}

// Nodes without the training label, and serverless virtual nodes that publish
// an elastic GPU allowance, must never inflate the budget.
func TestTrainingPoolCapacityIgnoresUnlabelledAndVirtualNodes(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		trainingNode("gpu-1", trainingPool, "8", "64", "512Gi"),
		trainingNode("cpu-only", map[string]string{"role": "infra"}, "0", "32", "128Gi"),
		trainingNode("vci-a", map[string]string{"accelerator": "nvidia-rtx-4090", "type": "virtual-kubelet"}, "1000", "1000", "4096Gi"),
	)}

	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.GPUs != 8 || capacity.Nodes != 1 {
		t.Fatalf("only the real labelled GPU node counts, got %+v", capacity)
	}
}

func TestTrainingPoolCapacitySkipsNotReadyNodes(t *testing.T) {
	notReady := trainingNode("gpu-down", trainingPool, "8", "64", "512Gi")
	notReady.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	ready := trainingNode("gpu-1", trainingPool, "8", "64", "512Gi")
	ready.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}

	client := &Client{kubernetes: fake.NewSimpleClientset(ready, notReady)}
	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.GPUs != 8 {
		t.Fatalf("a NotReady node must not be counted, got %+v", capacity)
	}
}
