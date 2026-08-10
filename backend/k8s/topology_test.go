package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListGPUNodeUsageAggregatesAllocationsFromPods(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}, Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}, Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "ray-worker"}, Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}}}}}},
	)}
	items, err := client.ListGPUNodeUsage(context.Background())
	if err != nil {
		t.Fatalf("list GPU usage: %v", err)
	}
	if len(items) != 1 || items[0].Capacity != 8 || items[0].Allocated != 4 || items[0].Available != 4 {
		t.Fatalf("unexpected GPU usage: %+v", items)
	}
}
