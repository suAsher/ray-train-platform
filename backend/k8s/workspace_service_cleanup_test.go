package k8s

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// Stopping a workspace deleted the RayCluster but never its Service, so every
// stop leaked one. A cluster that had run workspaces for a week held 22
// endpoint-less Services.
func TestDeleteWorkspaceServiceRemovesTheServiceLeftByAStoppedWorkspace(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	client := NewClientFromInterfaces(nil, clientset)
	ctx := context.Background()

	if err := client.EnsureWorkspaceService(ctx, "tenant-a", "dev-abc", "workspace-1"); err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	if err := client.DeleteWorkspaceService(ctx, "tenant-a", "dev-abc", "workspace-1"); err != nil {
		t.Fatalf("delete workspace service: %v", err)
	}
	_, err := clientset.CoreV1().Services("tenant-a").Get(ctx, "dev-abc-dev-svc", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected the service to be gone, got %v", err)
	}
}

// The same ownership rule as the RayCluster: one workspace must never remove
// another's Service, which would black-hole a running editor session.
func TestDeleteWorkspaceServiceRefusesAnotherWorkspacesService(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	client := NewClientFromInterfaces(nil, clientset)
	ctx := context.Background()

	if err := client.EnsureWorkspaceService(ctx, "tenant-a", "dev-abc", "workspace-1"); err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	if err := client.DeleteWorkspaceService(ctx, "tenant-a", "dev-abc", "workspace-2"); err == nil {
		t.Fatal("expected a refusal to delete another workspace's service")
	}
	if _, err := clientset.CoreV1().Services("tenant-a").Get(ctx, "dev-abc-dev-svc", metav1.GetOptions{}); err != nil {
		t.Fatalf("the service must survive: %v", err)
	}
}

// Stopping an already-stopped workspace is a normal retry and must succeed.
func TestDeleteWorkspaceServiceIsIdempotent(t *testing.T) {
	client := NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())
	if err := client.DeleteWorkspaceService(context.Background(), "tenant-a", "dev-missing", "workspace-1"); err != nil {
		t.Fatalf("deleting an absent service must not fail: %v", err)
	}
}
