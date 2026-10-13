package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestDevRayClusterCompactPreferenceIsWorkerOnlyAndSoft(t *testing.T) {
	options := WorkspaceRenderOptions{
		WorkerNodeAffinityPreference: []string{"node-b", "node-a"},
		DedicatedNodes:               map[string][]string{"team-a": {"node-a", "node-b"}},
	}
	for _, group := range []string{"head", "worker"} {
		spec := devClusterPodSpec(t, options, group)
		affinity := spec["affinity"].(map[string]any)["nodeAffinity"].(map[string]any)
		if affinity["requiredDuringSchedulingIgnoredDuringExecution"] == nil {
			t.Fatalf("%s lost its dedicated required policy", group)
		}
		preferred, _ := affinity["preferredDuringSchedulingIgnoredDuringExecution"].([]any)
		if group == "head" {
			if len(preferred) != 0 {
				t.Fatal("CPU-only debug head must not receive GPU packing hints")
			}
			continue
		}
		if len(preferred) != 2 {
			t.Fatalf("worker preferences=%v", preferred)
		}
		first := preferred[0].(map[string]any)
		values := first["preference"].(map[string]any)["matchFields"].([]any)[0].(map[string]any)["values"].([]any)
		if first["weight"] != int64(100) || values[0] != "node-b" {
			t.Fatalf("wrong compact priority: %v", first)
		}
		if spec["nodeName"] != nil {
			t.Fatal("soft compact placement must not bind a physical node")
		}
	}
}

func TestDevRayClusterZeroGPUDoesNotReceiveGPUCompactPreference(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-zero", TenantID: "team-a", UserID: "user-a", Name: "dev-zero", Namespace: "tenant-team-a", GPUCount: 0}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
		Image: "registry.example/ray@sha256:" + strings.Repeat("0", 64), WorkerNodeAffinityPreference: []string{"gpu-node"},
	})
	if err != nil {
		t.Fatal(err)
	}
	groups, _, _ := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
	spec := groups[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if spec["affinity"] != nil {
		t.Fatalf("zero-GPU debugging must not favor GPU-occupied nodes: %v", spec["affinity"])
	}
}
