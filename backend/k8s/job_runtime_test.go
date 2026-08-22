package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestListJobRuntimeIncludesOnlyOwnedPods(t *testing.T) {
	gpu := resource.MustParse("8")
	client := &Client{kubernetes: k8sfake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "train-head", Namespace: "tenant-a",
				Labels: map[string]string{"platform_job_id": "job-01", "ray.io/node-type": "head"},
			},
			Spec: corev1.PodSpec{
				NodeName: "gpu-node-a",
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): gpu},
				}}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.10"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "train-worker", Namespace: "tenant-a",
				Labels: map[string]string{"platform_job_id": "job-01", "ray.io/node-type": "worker"},
			},
			Spec:   corev1.PodSpec{NodeName: "gpu-node-b"},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "another-job", Namespace: "tenant-a",
				Labels: map[string]string{"platform_job_id": "job-other"},
			},
		},
	)}

	runtime, err := client.ListJobRuntime(context.Background(), "tenant-a", "job-01")
	if err != nil {
		t.Fatalf("list job runtime: %v", err)
	}
	if runtime.Namespace != "tenant-a" || runtime.JobID != "job-01" {
		t.Fatalf("unexpected runtime identity: %+v", runtime)
	}
	if len(runtime.Pods) != 2 {
		t.Fatalf("expected two owned pods, got %+v", runtime.Pods)
	}
	if head := runtime.Pods[0]; head.Name != "train-head" || head.Role != "head" || head.NodeName != "gpu-node-a" || head.GPURequested != 8 {
		t.Fatalf("unexpected head runtime: %+v", head)
	}
	if worker := runtime.Pods[1]; worker.Name != "train-worker" || worker.Role != "worker" || worker.Phase != "Pending" {
		t.Fatalf("unexpected worker runtime: %+v", worker)
	}
}
