package k8s

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveRayDashboardServiceFindsHeadDashboardPort(t *testing.T) {
	client := NewClientFromInterfaces(nil, fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "train-a-head-svc", Namespace: "tenant-a", Labels: map[string]string{"ray.io/cluster": "train-a", "ray.io/node-type": "head"}},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "client", Port: 10001}, {Name: "dashboard", Port: 8265}}},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "train-a-worker", Namespace: "tenant-a", Labels: map[string]string{"ray.io/cluster": "train-a", "ray.io/node-type": "worker"}},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "dashboard", Port: 8265}}},
		},
	))

	upstream, err := client.ResolveRayDashboardService(context.Background(), "tenant-a", "train-a")
	if err != nil {
		t.Fatalf("resolve dashboard service: %v", err)
	}
	if upstream != "http://train-a-head-svc.tenant-a.svc.cluster.local:8265" {
		t.Fatalf("unexpected upstream %q", upstream)
	}
}

func TestResolveRayDashboardServiceRequiresLiveHeadService(t *testing.T) {
	client := NewClientFromInterfaces(nil, fake.NewSimpleClientset())
	_, err := client.ResolveRayDashboardService(context.Background(), "tenant-a", "train-a")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}
