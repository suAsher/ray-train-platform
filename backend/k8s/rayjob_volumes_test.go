package k8s

import (
	"strings"
	"testing"
)

func headPodSpec(t *testing.T) map[string]any {
	t.Helper()
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, found, err := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if err != nil || !found {
		t.Fatalf("read head pod spec: %v", err)
	}
	return spec
}

// Ray writes its session directory to /tmp/ray. Mounting a volume *inside*
// that path makes the kubelet create /tmp/ray as root, and the Ray container
// (uid 1000) then fails to start with
// "PermissionError: [Errno 13] Permission denied: '/tmp/ray/session_...'".
func TestRayPodsDoNotMountInsideRayTempDir(t *testing.T) {
	spec := headPodSpec(t)
	containers, _ := spec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatalf("expected a ray container")
	}
	container, _ := containers[0].(map[string]any)
	mounts, _ := container["volumeMounts"].([]any)
	if len(mounts) == 0 {
		t.Fatalf("expected volume mounts")
	}
	for _, item := range mounts {
		mount, _ := item.(map[string]any)
		path, _ := mount["mountPath"].(string)
		if path == "/tmp/ray" || strings.HasPrefix(path, "/tmp/ray/") {
			t.Fatalf("mount %q shadows Ray's temp directory and breaks startup", path)
		}
	}
}

func TestRayPodsStillProvideSpillAndSharedMemory(t *testing.T) {
	spec := headPodSpec(t)
	containers, _ := spec["containers"].([]any)
	container, _ := containers[0].(map[string]any)
	mounts, _ := container["volumeMounts"].([]any)

	paths := make(map[string]bool)
	for _, item := range mounts {
		mount, _ := item.(map[string]any)
		path, _ := mount["mountPath"].(string)
		paths[path] = true
	}
	if !paths["/dev/shm"] {
		t.Fatalf("shared memory mount is required for multi-process training, got %v", paths)
	}
	if !paths["/workspace"] {
		t.Fatalf("workspace mount is required, got %v", paths)
	}
}
