package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestEnsureWorkspaceServiceTargetsGPUWorker(t *testing.T) {
	client := &Client{kubernetes: k8sfake.NewSimpleClientset()}
	if err := client.EnsureWorkspaceService(context.Background(), "tenant-a", "debug-a", "ws-1"); err != nil {
		t.Fatalf("ensure workspace service: %v", err)
	}
	service, err := client.kubernetes.CoreV1().Services("tenant-a").Get(context.Background(), "debug-a-dev-svc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get GPU worker service: %v", err)
	}
	if service.Spec.Selector["ray.io/cluster"] != "debug-a" || service.Spec.Selector["ray.io/node-type"] != "worker" {
		t.Fatalf("interactive service must select the GPU worker, got %v", service.Spec.Selector)
	}
	if !hasServicePort(service.Spec.Ports, "jupyter", 8888) || !hasServicePort(service.Spec.Ports, "vscode", 8443) {
		t.Fatalf("interactive service must expose JupyterLab and VS Code, got %v", service.Spec.Ports)
	}
}

func hasServicePort(ports []corev1.ServicePort, name string, port int32) bool {
	for _, candidate := range ports {
		if candidate.Name == name && candidate.Port == port {
			return true
		}
	}
	return false
}
