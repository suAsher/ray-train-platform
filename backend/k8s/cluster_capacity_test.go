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
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
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
		trainingNode("gpu-2", trainingPool, "4", "64", "512Gi"),
		trainingNode("gpu-3", trainingPool, "2", "64", "512Gi"),
	)}

	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.GPUs != 14 {
		t.Fatalf("expected 14 GPUs across the Ready pool, got %d", capacity.GPUs)
	}
	if capacity.MaxGPUsPerNode != 8 {
		t.Fatalf("expected the largest Ready node to provide 8 GPUs, got %d", capacity.MaxGPUsPerNode)
	}
	if capacity.GuaranteedGPUsPerWorker != 2 {
		t.Fatalf("expected every counted node to place a 2-GPU worker, got %d", capacity.GuaranteedGPUsPerWorker)
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
	if capacity.GPUs != 8 || capacity.Nodes != 1 || capacity.MaxGPUsPerNode != 8 || capacity.GuaranteedGPUsPerWorker != 8 {
		t.Fatalf("only the real labelled GPU node counts, got %+v", capacity)
	}
}

func TestTrainingPoolCapacitySkipsNotReadyNodes(t *testing.T) {
	notReady := trainingNode("gpu-down", trainingPool, "100", "64", "512Gi")
	notReady.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	ready := trainingNode("gpu-1", trainingPool, "8", "64", "512Gi")
	ready.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}

	client := &Client{kubernetes: fake.NewSimpleClientset(ready, notReady)}
	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.GPUs != 8 || capacity.Nodes != 1 || capacity.MaxGPUsPerNode != 8 || capacity.GuaranteedGPUsPerWorker != 8 {
		t.Fatalf("a NotReady node must not be counted, got %+v", capacity)
	}
}

func TestTrainingPoolCapacitySkipsNodesWithoutReadyCondition(t *testing.T) {
	unknown := trainingNode("gpu-unknown", trainingPool, "8", "64", "512Gi")
	unknown.Status.Conditions = nil

	client := &Client{kubernetes: fake.NewSimpleClientset(unknown)}
	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.Nodes != 0 || capacity.GPUs != 0 || capacity.MaxGPUsPerNode != 0 || capacity.GuaranteedGPUsPerWorker != 0 {
		t.Fatalf("a node without an explicit Ready condition must not be counted, got %+v", capacity)
	}
}

func TestTrainingPoolCapacitySkipsUnschedulableAndZeroGPUNodes(t *testing.T) {
	unschedulable := trainingNode("gpu-cordoned", trainingPool, "8", "128", "1Ti")
	unschedulable.Spec.Unschedulable = true
	zeroGPU := trainingNode("gpu-empty", trainingPool, "0", "256", "2Ti")
	readyGPU := trainingNode("gpu-ready", trainingPool, "4", "16", "64Gi")

	client := &Client{kubernetes: fake.NewSimpleClientset(unschedulable, zeroGPU, readyGPU)}
	capacity, err := client.TrainingPoolCapacity(context.Background(), trainingPool)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	if capacity.Nodes != 1 || capacity.GPUs != 4 || capacity.MaxGPUsPerNode != 4 || capacity.GuaranteedGPUsPerWorker != 4 {
		t.Fatalf("only the schedulable positive-GPU node should define pool shape, got %+v", capacity)
	}
	if capacity.CPUMillis != 16_000 || capacity.MemoryBytes != 64*1024*1024*1024 {
		t.Fatalf("excluded nodes must not inflate CPU or memory, got %+v", capacity)
	}
}

func TestTrainingPoolCapacityUsesRenderDefaultSelectorWhenEmpty(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		trainingNode("default-pool", defaultTrainingNodeSelector, "8", "64", "512Gi"),
		trainingNode("other-pool", map[string]string{"pool": "other"}, "100", "128", "1Ti"),
	)}

	selectors := []map[string]string{nil, {}}
	for _, selector := range selectors {
		capacity, err := client.TrainingPoolCapacity(context.Background(), selector)
		if err != nil {
			t.Fatalf("read capacity: %v", err)
		}
		if capacity.Nodes != 1 || capacity.GPUs != 8 || capacity.MaxGPUsPerNode != 8 || capacity.GuaranteedGPUsPerWorker != 8 {
			t.Fatalf("empty selector must discover only the default Ray training pool, got %+v", capacity)
		}
	}
}
