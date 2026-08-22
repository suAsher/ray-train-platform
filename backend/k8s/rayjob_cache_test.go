package k8s

import (
	"strings"
	"testing"
)

func TestRenderRayJobAddsGenericEphemeralCacheOnlyWhenConfigured(t *testing.T) {
	options := testRenderOptions()
	options.LocalCache = LocalCacheOptions{
		Enabled:      true,
		StorageClass: "ray-cache-local",
		Size:         "200Gi",
		MountPath:    "/mnt/cache",
	}
	manifest, err := RenderRayJob(validRenderJob(), options)
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}

	headSpec := cacheHeadPodSpec(t, manifest.Object)
	workerSpec := cacheWorkerPodSpec(t, manifest.Object)
	submitterSpec := cacheSubmitterPodSpec(t, manifest.Object)
	for name, podSpec := range map[string]map[string]any{"head": headSpec, "worker": workerSpec} {
		assertGenericEphemeralCache(t, name, podSpec)
		if got := podEnvironment(podSpec)["PLATFORM_CACHE_PATH"]; got != "/mnt/cache" {
			t.Fatalf("%s cache path: got %q", name, got)
		}
	}
	if strings.Contains(string(mustJSON(submitterSpec)), "local-cache") || podEnvironment(submitterSpec)["PLATFORM_CACHE_PATH"] != "" {
		t.Fatalf("submitter must not receive node-local cache: %s", mustJSON(submitterSpec))
	}

	headStart, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "rayStartParams")
	if headStart["temp-dir"] != "/mnt/cache/ray" || !strings.Contains(headStart["object-spilling-config"].(string), "/mnt/cache/ray-spill") {
		t.Fatalf("head must keep Ray session and spills on cache: %#v", headStart)
	}
	workers, _, _ := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	workerStart, _ := worker["rayStartParams"].(map[string]any)
	if workerStart["temp-dir"] != "/mnt/cache/ray" || !strings.Contains(workerStart["object-spilling-config"].(string), "/mnt/cache/ray-spill") {
		t.Fatalf("worker must keep Ray session and spills on cache: %#v", workerStart)
	}
}

func TestRenderRayJobOmitsLocalCacheWhenDisabled(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	for name, podSpec := range map[string]map[string]any{
		"head":      cacheHeadPodSpec(t, manifest.Object),
		"worker":    cacheWorkerPodSpec(t, manifest.Object),
		"submitter": cacheSubmitterPodSpec(t, manifest.Object),
	} {
		if strings.Contains(string(mustJSON(podSpec)), "local-cache") || podEnvironment(podSpec)["PLATFORM_CACHE_PATH"] != "" {
			t.Fatalf("%s unexpectedly receives local cache: %s", name, mustJSON(podSpec))
		}
	}
}

func TestRenderRayJobRejectsCacheMountedInsideRayDefaultTempDirectory(t *testing.T) {
	options := testRenderOptions()
	options.LocalCache = LocalCacheOptions{
		Enabled:      true,
		StorageClass: "ray-cache-local",
		Size:         "200Gi",
		MountPath:    "/tmp/ray",
	}
	if _, err := RenderRayJob(validRenderJob(), options); err == nil || !strings.Contains(err.Error(), "mount path") {
		t.Fatalf("expected unsafe Ray temp-directory cache mount to be rejected, got %v", err)
	}
}

func assertGenericEphemeralCache(t *testing.T, podName string, podSpec map[string]any) {
	t.Helper()
	volumes, _, _ := nestedSlice(podSpec, "volumes")
	foundVolume := false
	for _, item := range volumes {
		volume := item.(map[string]any)
		if volume["name"] != "local-cache" {
			continue
		}
		ephemeral, ok := volume["ephemeral"].(map[string]any)
		if !ok {
			t.Fatalf("%s cache must use a generic ephemeral volume: %#v", podName, volume)
		}
		claim := ephemeral["volumeClaimTemplate"].(map[string]any)
		spec := claim["spec"].(map[string]any)
		requests := spec["resources"].(map[string]any)["requests"].(map[string]any)
		if spec["storageClassName"] == "ray-cache-local" && requests["storage"] == "200Gi" {
			foundVolume = true
		}
	}
	if !foundVolume {
		t.Fatalf("%s generic ephemeral cache volume missing or malformed: %#v", podName, volumes)
	}

	containers, _, _ := nestedSlice(podSpec, "containers")
	mounts := containers[0].(map[string]any)["volumeMounts"].([]any)
	paths := map[string]bool{}
	for _, item := range mounts {
		mount := item.(map[string]any)
		if mount["name"] == "local-cache" {
			paths[mount["mountPath"].(string)] = true
		}
	}
	if !paths["/mnt/cache"] {
		t.Fatalf("%s cache must be mounted for staging and Ray spills: %#v", podName, mounts)
	}
	securityContext := podSpec["securityContext"].(map[string]any)
	if securityContext["fsGroup"] != int64(1000) {
		t.Fatalf("%s cache must be writable by the non-root Ray user: %#v", podName, securityContext)
	}
}

func cacheHeadPodSpec(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	podSpec, found, err := nestedMap(object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if err != nil || !found {
		t.Fatalf("head pod spec: found=%t err=%v", found, err)
	}
	return podSpec
}

func cacheWorkerPodSpec(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	workers, found, err := nestedSlice(object, "spec", "rayClusterSpec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("worker groups: found=%t err=%v groups=%#v", found, err, workers)
	}
	podSpec, found, err := nestedMap(workers[0].(map[string]any), "template", "spec")
	if err != nil || !found {
		t.Fatalf("worker pod spec: found=%t err=%v", found, err)
	}
	return podSpec
}

func cacheSubmitterPodSpec(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	podSpec, found, err := nestedMap(object, "spec", "submitterPodTemplate", "spec")
	if err != nil || !found {
		t.Fatalf("submitter pod spec: found=%t err=%v", found, err)
	}
	return podSpec
}
