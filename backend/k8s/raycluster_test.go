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
	if manifest.GetLabels()["app.kubernetes.io/part-of"] != "ray-train-platform" {
		t.Fatalf("RayCluster must carry the platform ownership label: %#v", manifest.GetLabels())
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
	security, _, _ := nestedMap(manifest.Object, "spec", "headGroupSpec", "template", "spec", "securityContext")
	if security["fsGroup"] != int64(1000) || security["fsGroupChangePolicy"] != "OnRootMismatch" {
		t.Fatalf("debug workspace must make its non-root writable mount explicit: %#v", security)
	}
}

func TestRenderDevRayClusterRunsInteractiveToolsOnTheGPUWorker(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", GPUCount: 1, State: domain.WorkspaceSubmitted}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: "registry.example/dev@sha256:" + strings.Repeat("a", 64), RayVersion: "2.35.0", JupyterBasePath: "/api/v1/dev-workspaces/ws-1/proxy/"})
	if err != nil {
		t.Fatalf("render dev cluster: %v", err)
	}
	head, found, err := nestedMap(manifest.Object, "spec", "headGroupSpec")
	if err != nil || !found {
		t.Fatalf("read head group: %v", err)
	}
	rayStart, _ := head["rayStartParams"].(map[string]any)
	if rayStart["num-gpus"] != "0" {
		t.Fatalf("the Ray control-plane head must not reserve a GPU, got %v", rayStart)
	}
	containers, found, err := nestedSlice(manifest.Object, "spec", "headGroupSpec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("read head container: found=%v err=%v", found, err)
	}
	headContainer, _ := containers[0].(map[string]any)
	headResources, _ := headContainer["resources"].(map[string]any)
	headLimits, _ := headResources["limits"].(map[string]any)
	if _, found := headLimits["nvidia.com/gpu"]; found {
		t.Fatalf("the Ray control-plane head must not request a GPU, got limits=%v", headLimits)
	}
	args, _ := headContainer["args"].([]any)
	if len(args) != 1 || strings.Contains(args[0].(string), "code-server") || strings.Contains(args[0].(string), "jupyter lab") {
		t.Fatalf("interactive tools must not run in the Ray head, got %v", args)
	}
	workers, found, err := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("interactive debugging must create one GPU worker, got %v", workers)
	}
	worker, _ := workers[0].(map[string]any)
	workerRayStart, _ := worker["rayStartParams"].(map[string]any)
	if workerRayStart["num-gpus"] != "1" {
		t.Fatalf("the worker must register its GPU with Ray, got %v", workerRayStart)
	}
	workerContainers, found, err := nestedSlice(worker, "template", "spec", "containers")
	if err != nil || !found || len(workerContainers) != 1 {
		t.Fatalf("read worker container: found=%v err=%v", found, err)
	}
	workerContainer, _ := workerContainers[0].(map[string]any)
	resources, _ := workerContainer["resources"].(map[string]any)
	limits, _ := resources["limits"].(map[string]any)
	if limits["nvidia.com/gpu"] != "1" {
		t.Fatalf("JupyterLab and VS Code must run in the GPU pod, got limits=%v", limits)
	}
	lifecycle, _ := workerContainer["lifecycle"].(map[string]any)
	postStart, _ := lifecycle["postStart"].(map[string]any)
	exec, _ := postStart["exec"].(map[string]any)
	command, _ := exec["command"].([]any)
	if len(command) != 3 {
		t.Fatalf("worker must launch interactive tools with a post-start hook, got %v", command)
	}
	toolCommand, _ := command[2].(string)
	if !strings.Contains(toolCommand, "mkdir -p /tmp/ray-platform; nohup code-server") || !strings.Contains(toolCommand, "code-server --bind-addr 0.0.0.0:8443") || !strings.Contains(toolCommand, "jupyter lab --ip=0.0.0.0 --port=8888") || !strings.Contains(toolCommand, "--ServerApp.base_url='/api/v1/dev-workspaces/ws-1/proxy/'") {
		t.Fatalf("GPU worker must start VS Code and JupyterLab with the workspace proxy path, got %q", toolCommand)
	}
}

func TestRenderDevRayClusterAllowsMultiGPUWorker(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", GPUCount: 2, State: domain.WorkspaceSubmitted}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: "registry.example/dev@sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatalf("render two-GPU workspace: %v", err)
	}
	workers, found, err := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("read worker group: found=%v err=%v", found, err)
	}
	worker := workers[0].(map[string]any)
	if worker["replicas"] != int64(1) {
		t.Fatalf("multi-GPU workspace must keep one interactive worker: %#v", worker)
	}
	start, _ := worker["rayStartParams"].(map[string]any)
	if start["num-gpus"] != "2" {
		t.Fatalf("worker must advertise both GPUs to Ray: %#v", start)
	}
	containers, _, _ := nestedSlice(worker, "template", "spec", "containers")
	resources, _ := containers[0].(map[string]any)["resources"].(map[string]any)
	limits, _ := resources["limits"].(map[string]any)
	if limits["nvidia.com/gpu"] != "2" {
		t.Fatalf("worker must request two GPUs: %#v", limits)
	}
}

func TestRenderDevRayClusterPullsTaggedImagesEveryStart(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", GPUCount: 1, State: domain.WorkspaceSubmitted}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: "registry.example/team/workspace:cuda121"})
	if err != nil {
		t.Fatalf("render tagged dev image: %v", err)
	}
	for group, path := range map[string][]string{
		"head":   {"spec", "headGroupSpec", "template", "spec", "containers"},
		"worker": {"spec", "workerGroupSpecs", "0", "template", "spec", "containers"},
	} {
		var containers []any
		if group == "head" {
			containers, _, _ = nestedSlice(manifest.Object, path...)
		} else {
			workers, _, _ := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
			worker := workers[0].(map[string]any)
			containers, _, _ = nestedSlice(worker, "template", "spec", "containers")
		}
		container := containers[0].(map[string]any)
		if container["imagePullPolicy"] != "Always" {
			t.Fatalf("%s must refresh a tagged image, got %#v", group, container)
		}
	}
}

// Once a personal data mount is ready, both editors must open the same
// persistent workspace that will later be snapshotted for a training job.
// Opening /home/ray would make the user start in an ephemeral image layer and
// hides the platform's intended /workspace contract.
func TestRenderDevRayClusterStartsEditorsInPersistentWorkspace(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", GPUCount: 1, State: domain.WorkspaceSubmitted}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
		Image:      "registry.example/dev@sha256:" + strings.Repeat("a", 64),
		DataMounts: DataMountPlan{Personal: &DataMountRoot{ClaimName: "data-user-a", ReadOnly: false}},
	})
	if err != nil {
		t.Fatalf("render dev cluster: %v", err)
	}
	workers, found, err := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("read worker group: found=%v err=%v", found, err)
	}
	worker := workers[0].(map[string]any)
	containers, found, err := nestedSlice(worker, "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("read worker container: found=%v err=%v", found, err)
	}
	lifecycle, _ := containers[0].(map[string]any)["lifecycle"].(map[string]any)
	postStart, _ := lifecycle["postStart"].(map[string]any)
	exec, _ := postStart["exec"].(map[string]any)
	command, _ := exec["command"].([]any)
	toolCommand, _ := command[2].(string)
	for _, expected := range []string{
		"code-server --bind-addr 0.0.0.0:8443 --auth none --disable-telemetry /workspace",
		"--ServerApp.root_dir='/workspace'",
	} {
		if !strings.Contains(toolCommand, expected) {
			t.Fatalf("persistent workspace editor command is missing %q: %q", expected, toolCommand)
		}
	}
}

func TestRenderDevRayClusterAvoidsLegacyIDCClaimWhenGovernedIDCIsPresent(t *testing.T) {
	workspace := domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", GPUCount: 1, State: domain.WorkspaceSubmitted}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
		Image:            "registry.example/dev@sha256:" + strings.Repeat("a", 64),
		IDCExistingClaim: "legacy-idc-rwx",
		DataMounts: DataMountPlan{
			IDCOriginal: &DataMountRoot{ClaimName: "idc-original-ro", ReadOnly: true},
		},
	})
	if err != nil {
		t.Fatalf("render dev cluster: %v", err)
	}
	for group, pod := range map[string]map[string]any{
		"head":   dataMountDevPodSpec(t, manifest, "head"),
		"worker": dataMountDevPodSpec(t, manifest, "worker"),
	} {
		assertStorageMount(t, pod, "platform-data-idc-original", "idc-original-ro", domain.IDCOriginalMountPath, true)
		if hasVolumeOrMountNamed(pod, "idc-storage") {
			t.Fatalf("%s pod must not retain the legacy writable IDC mount when governed IDC is available: %#v", group, pod)
		}
	}
}

func hasVolumeOrMountNamed(podSpec map[string]any, name string) bool {
	volumes, _, _ := nestedSlice(podSpec, "volumes")
	for _, value := range volumes {
		if volume, _ := value.(map[string]any); volume["name"] == name {
			return true
		}
	}
	containers, _, _ := nestedSlice(podSpec, "containers")
	for _, value := range containers {
		container, _ := value.(map[string]any)
		for _, mountValue := range container["volumeMounts"].([]any) {
			if mount, _ := mountValue.(map[string]any); mount["name"] == name {
				return true
			}
		}
	}
	return false
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
