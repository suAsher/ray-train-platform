package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func TestRenderDevRayClusterUsesPVCAndProtectedInternalJupyter(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", GPUCount: 1, State: domain.WorkspaceSubmitted}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: "registry.example/dev@sha256:" + strings.Repeat("a", 64), RayVersion: "2.35.0", IDCExistingClaim: "idc-rwx"})
	if err != nil {
		t.Fatalf("render dev cluster: %v", err)
	}
	if manifest.GetKind() != "RayCluster" || manifest.GetName() != "debug-a" {
		t.Fatalf("unexpected manifest metadata: %s/%s", manifest.GetKind(), manifest.GetName())
	}
	if _, found, _ := nestedSlice(manifest.Object, "spec", "headGroupSpec", "template", "spec", "containers"); !found {
		t.Fatal("expected head container")
	}
	headSpec, ok, _ := nestedMap(manifest.Object, "spec", "headGroupSpec", "template", "spec")
	if !ok || headSpec["automountServiceAccountToken"] != false {
		t.Fatalf("debug pods must not mount Kubernetes API tokens by default: %#v", headSpec)
	}
	if _, found, _ := nestedMap(manifest.Object, "spec", "headGroupSpec", "template", "spec", "volumes", "0"); found {
		t.Fatal("volumes must be an array")
	}
	if strings.Contains(string(mustJSON(manifest.Object)), "hostPath") {
		t.Fatal("dev cluster must not use hostPath")
	}
}

func TestMapRayClusterState(t *testing.T) {
	ready := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"state": "Ready"}}}
	if got := MapRayClusterState(ready); got != "RUNNING" {
		t.Fatalf("expected running state, got %s", got)
	}
	failed := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"state": "Failed"}}}
	if got := MapRayClusterState(failed); got != "FAILED" {
		t.Fatalf("expected failed state, got %s", got)
	}
}
