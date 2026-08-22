package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func devClusterPodSpec(t *testing.T, options WorkspaceRenderOptions, group string) map[string]any {
	t.Helper()
	workspace := domain.DevWorkspace{
		ID: "ws-1", TenantID: "team-a", UserID: "user-1",
		Name: "dev-1", Namespace: "tenant-team-a", RayClusterName: "dev-1", GPUCount: 1,
	}
	if options.Image == "" {
		options.Image = "registry.example/ray@sha256:" + strings.Repeat("0", 64)
	}
	manifest, err := RenderDevRayCluster(workspace, options)
	if err != nil {
		t.Fatalf("render dev cluster: %v", err)
	}
	if group == "worker" {
		groups, _, err := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
		if err != nil || len(groups) == 0 {
			t.Fatalf("read worker groups: %v", err)
		}
		worker, _ := groups[0].(map[string]any)
		template, _ := worker["template"].(map[string]any)
		spec, _ := template["spec"].(map[string]any)
		return spec
	}
	spec, found, err := nestedMap(manifest.Object, "spec", "headGroupSpec", "template", "spec")
	if err != nil || !found {
		t.Fatalf("read head pod spec: %v", err)
	}
	return spec
}

// The debug cluster's head runs JupyterLab and the Ray GCS. Left unconstrained
// it can be scheduled onto a serverless virtual node, where it cannot pull the
// image from the internal registry and the workspace never starts.
func TestDevRayClusterHeadIsPinnedToTheTrainingPool(t *testing.T) {
	spec := devClusterPodSpec(t, WorkspaceRenderOptions{NodeSelector: map[string]string{"accelerator": "nvidia-h100"}}, "head")
	selector, _ := spec["nodeSelector"].(map[string]any)
	if selector["accelerator"] != "nvidia-h100" {
		t.Fatalf("dev head must carry the training node selector, got %v", selector)
	}
}

func TestDevRayClusterWorkerUsesConfiguredSelector(t *testing.T) {
	spec := devClusterPodSpec(t, WorkspaceRenderOptions{NodeSelector: map[string]string{"accelerator": "nvidia-h100"}}, "worker")
	selector, _ := spec["nodeSelector"].(map[string]any)
	if selector["accelerator"] != "nvidia-h100" {
		t.Fatalf("GPU worker must use the configured selector, got %v", selector)
	}
}

func TestDevRayClusterFallsBackToDefaultSelector(t *testing.T) {
	for _, group := range []string{"head", "worker"} {
		spec := devClusterPodSpec(t, WorkspaceRenderOptions{}, group)
		selector, _ := spec["nodeSelector"].(map[string]any)
		if selector["accelerator"] != "nvidia-rtx-4090" {
			t.Fatalf("%s should default to the training label, got %v", group, selector)
		}
	}
}
