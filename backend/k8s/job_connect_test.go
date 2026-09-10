package k8s

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveJobWorkerSelectsOnlyRunningWorkersInStableOrder(t *testing.T) {
	client := NewClientFromInterfaces(nil, fake.NewSimpleClientset(
		jobConnectPod("worker-b", "worker", corev1.PodRunning),
		jobConnectPod("head", "head", corev1.PodRunning),
		jobConnectPod("worker-a", "worker", corev1.PodRunning),
	))

	target, err := client.ResolveJobWorker(context.Background(), "tenant-a", "job-a", 0)
	if err != nil {
		t.Fatalf("resolve worker: %v", err)
	}
	if target.PodName != "worker-a" || target.ContainerName != "ray-worker" {
		t.Fatalf("unexpected worker target: %+v", target)
	}
}

func TestResolveJobWorkerRejectsPendingOrMissingOrdinal(t *testing.T) {
	client := NewClientFromInterfaces(nil, fake.NewSimpleClientset(
		jobConnectPod("worker-a", "worker", corev1.PodPending),
	))

	if _, err := client.ResolveJobWorker(context.Background(), "tenant-a", "job-a", 0); !errors.Is(err, ErrJobWorkerNotRunning) {
		t.Fatalf("expected not-running error, got %v", err)
	}
	if _, err := client.ResolveJobWorker(context.Background(), "tenant-a", "job-a", 1); !errors.Is(err, ErrJobWorkerNotFound) {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func jobConnectPod(name, role string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "tenant-a",
			Labels: map[string]string{platformJobIDLabel: "job-a", "ray.io/node-type": role},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "ray-worker"}}},
		Status: corev1.PodStatus{Phase: phase},
	}
}
