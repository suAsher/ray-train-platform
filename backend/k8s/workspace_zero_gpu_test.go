package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func workspaceWorker(t *testing.T, gpuCount int) map[string]any {
	t.Helper()
	workspace := domain.DevWorkspace{
		ID: "ws-a", TenantID: "tenant-a", UserID: "user-a", Name: "ws-a",
		Namespace: "tenant-local", RayClusterName: "ws-a", GPUCount: gpuCount,
	}
	cluster, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: "example.invalid/image@sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatalf("render workspace: %v", err)
	}
	groups := cluster.Object["spec"].(map[string]any)["workerGroupSpecs"].([]any)
	if len(groups) == 0 {
		t.Fatal("worker group missing")
	}
	return groups[0].(map[string]any)
}

func workerContainer(t *testing.T, worker map[string]any) map[string]any {
	t.Helper()
	spec := worker["template"].(map[string]any)["spec"].(map[string]any)
	return spec["containers"].([]any)[0].(map[string]any)
}

// A debug session is most needed exactly when training holds every GPU, so the
// workspace must be able to reserve none. Requesting zero of an extended
// resource is legal but ambiguous; omitting the key states it plainly.
func TestZeroGPUWorkspaceRequestsNoDevice(t *testing.T) {
	container := workerContainer(t, workspaceWorker(t, 0))
	resources := container["resources"].(map[string]any)

	for _, section := range []string{"requests", "limits"} {
		values := resources[section].(map[string]any)
		if _, present := values["nvidia.com/gpu"]; present {
			t.Fatalf("a CPU-only workspace must not carry a GPU %s: %#v", section, values)
		}
		if values["cpu"] == nil || values["memory"] == nil {
			t.Fatalf("CPU and memory must still be reserved: %#v", values)
		}
	}
}

// Under the NVIDIA runtime a container with no GPU request can still inherit
// NVIDIA_VISIBLE_DEVICES=all and reach every card on the node, including the
// ones a training job is using. Pinning it is what makes "no GPU" true.
func TestZeroGPUWorkspaceCannotSeeNeighbouringDevices(t *testing.T) {
	container := workerContainer(t, workspaceWorker(t, 0))

	var visibility string
	for _, entry := range container["env"].([]any) {
		item := entry.(map[string]any)
		if item["name"] == "NVIDIA_VISIBLE_DEVICES" {
			visibility, _ = item["value"].(string)
		}
	}
	if visibility != "void" {
		t.Fatalf("a CPU-only workspace must pin GPU visibility, got %q", visibility)
	}
}

// The existing sizes must keep reserving their devices, and a GPU workspace has
// no reason to have its visibility pinned.
func TestGPUWorkspaceStillReservesItsDevices(t *testing.T) {
	for _, gpuCount := range []int{1, 2, 4, 8} {
		container := workerContainer(t, workspaceWorker(t, gpuCount))
		requests := container["resources"].(map[string]any)["requests"].(map[string]any)
		if requests["nvidia.com/gpu"] == "" || requests["nvidia.com/gpu"] == nil {
			t.Fatalf("%d-GPU workspace lost its device request", gpuCount)
		}
		for _, entry := range container["env"].([]any) {
			if entry.(map[string]any)["name"] == "NVIDIA_VISIBLE_DEVICES" {
				t.Fatalf("%d-GPU workspace must not pin device visibility", gpuCount)
			}
		}
	}
}

// The workspace stays on a training node whatever its size: it mounts the same
// data and the operator asked for it to remain there.
func TestZeroGPUWorkspaceStaysOnTrainingNodes(t *testing.T) {
	spec := workspaceWorker(t, 0)["template"].(map[string]any)["spec"].(map[string]any)
	selector, ok := spec["nodeSelector"].(map[string]any)
	if !ok || len(selector) == 0 {
		t.Fatalf("a CPU-only workspace must keep the training node selector: %#v", spec["nodeSelector"])
	}
}
