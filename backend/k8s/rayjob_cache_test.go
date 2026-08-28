package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRenderRayJobAddsGenericEphemeralCacheOnlyWhenConfigured(t *testing.T) {
	options := runtimeCacheRenderOptions()
	job := validRenderJob()
	job.Spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "5Ti"}
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}

	headSpec := cacheHeadPodSpec(t, manifest.Object)
	workerSpec := cacheWorkerPodSpec(t, manifest.Object)
	submitterSpec := cacheSubmitterPodSpec(t, manifest.Object)
	assertGenericEphemeralCache(t, "head", headSpec, map[string]cacheVolumeExpectation{
		"local-cache-data1": {storageClass: "ray-cache-local-data1", size: "2560Gi", mountPath: "/mnt/cache"},
	})
	assertGenericEphemeralCache(t, "worker", workerSpec, map[string]cacheVolumeExpectation{
		"local-cache-data1": {storageClass: "ray-cache-local-data1", size: "2560Gi", mountPath: "/mnt/cache"},
		"local-cache-data2": {storageClass: "ray-cache-local-data2", size: "2560Gi", mountPath: "/mnt/cache2"},
	})
	for name, podSpec := range map[string]map[string]any{"head": headSpec, "worker": workerSpec} {
		if got := podEnvironment(podSpec)["PLATFORM_CACHE_PATH"]; got != "/mnt/cache" {
			t.Fatalf("%s compatibility cache path: got %q", name, got)
		}
	}
	if got := podEnvironment(workerSpec)["PLATFORM_CACHE_PATHS"]; got != "/mnt/cache:/mnt/cache2" {
		t.Fatalf("worker cache paths: got %q", got)
	}
	if got := podEnvironment(headSpec)["PLATFORM_CACHE_PATHS"]; got != "/mnt/cache" {
		t.Fatalf("head cache paths: got %q", got)
	}
	if strings.Contains(string(mustJSON(submitterSpec)), "local-cache") || podEnvironment(submitterSpec)["PLATFORM_CACHE_PATH"] != "" {
		t.Fatalf("submitter must not receive node-local cache: %s", mustJSON(submitterSpec))
	}

	headStart, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "rayStartParams")
	if headStart["temp-dir"] != "/mnt/cache/ray" {
		t.Fatalf("head must keep Ray session and spills on cache: %#v", headStart)
	}
	if _, exists := headStart["object-spilling-config"]; exists {
		t.Fatalf("Ray 2.35 does not accept --object-spilling-config: %#v", headStart)
	}
	workers, _, _ := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	workerStart, _ := worker["rayStartParams"].(map[string]any)
	if workerStart["temp-dir"] != "/mnt/cache/ray" {
		t.Fatalf("worker must keep Ray session and spills on cache: %#v", workerStart)
	}
	if _, exists := workerStart["object-spilling-config"]; exists {
		t.Fatalf("Ray 2.35 does not accept --object-spilling-config: %#v", workerStart)
	}
	for name, podSpec := range map[string]map[string]any{"head": headSpec, "worker": workerSpec} {
		spilling := podEnvironment(podSpec)["RAY_object_spilling_config"]
		if !strings.Contains(spilling, "/mnt/cache/ray-spill/objects") {
			t.Fatalf("%s must configure object spilling through the Ray-supported environment variable: %q", name, spilling)
		}
	}
}

func TestRenderRayJobAutomaticallyPreloadsSelectedInputOnWorkers(t *testing.T) {
	job := validRenderJob()
	job.Spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "1Ti", Preload: domain.CachePreloadInput}
	job.Spec.Input = domain.DataLocation{Space: domain.DataSpacePublic, RelativePath: "labeled/fz-v1"}
	job.Spec.ResolvedDataMounts.Input = &domain.ResolvedDataMount{
		Space: domain.DataSpacePublic, BindingSpace: domain.DataSpacePublic,
		ClaimName: "data-public", SubPath: "labeled/fz-v1",
		MountPath: domain.DataMountInputPath, ReadOnly: true,
	}
	manifest, err := RenderRayJob(job, runtimeCacheRenderOptions())
	if err != nil {
		t.Fatalf("render automatic preload job: %v", err)
	}

	worker := cacheWorkerPodSpec(t, manifest.Object)
	workerEnv := podEnvironment(worker)
	if workerEnv["PLATFORM_DATASET_SOURCE_PATH"] != domain.DataMountInputPath {
		t.Fatalf("worker must retain the durable source path: %#v", workerEnv)
	}
	if workerEnv["PLATFORM_DATASET_PATH"] != "/mnt/cache/dataset-view" {
		t.Fatalf("worker must read the platform-managed cached view: %#v", workerEnv)
	}
	if workerEnv["PLATFORM_CACHE_PRELOAD"] != "input" {
		t.Fatalf("worker must expose the preload contract: %#v", workerEnv)
	}

	initContainers, ok := worker["initContainers"].([]any)
	if !ok || len(initContainers) != 1 {
		t.Fatalf("worker must have exactly one platform cache preloader: %#v", worker["initContainers"])
	}
	preloader := initContainers[0].(map[string]any)
	if preloader["name"] != "dataset-cache-preloader" || preloader["image"] != runtimeCacheRenderOptions().SourceMaterializerImage {
		t.Fatalf("unexpected preloader identity: %#v", preloader)
	}
	preloaderEnv := environmentValues(preloader)
	if preloaderEnv["PLATFORM_DATASET_SOURCE_PATH"] != domain.DataMountInputPath || preloaderEnv["PLATFORM_CACHE_PATHS"] != "/mnt/cache:/mnt/cache2" {
		t.Fatalf("preloader paths are wrong: %#v", preloaderEnv)
	}
	if preloaderEnv["PLATFORM_CACHE_LIMIT_BYTES_PER_DISK"] != "549755813888" {
		t.Fatalf("preloader must enforce the requested 512 GiB per disk: %#v", preloaderEnv)
	}
	metricEnv := metricEnvironment(preloader)
	if metricEnv["PLATFORM_JOB_ID"]["value"] != "job-01" {
		t.Fatalf("preloader missing immutable job ID: %#v", metricEnv)
	}
	for name, field := range map[string]string{"PLATFORM_POD_NAMESPACE": "metadata.namespace", "PLATFORM_POD_NAME": "metadata.name", "PLATFORM_RAY_CLUSTER": "metadata.labels['ray.io/cluster']", "PLATFORM_RAY_NODE_TYPE": "metadata.labels['ray.io/node-type']"} {
		valueFrom, _ := metricEnv[name]["valueFrom"].(map[string]any)
		fieldRef, _ := valueFrom["fieldRef"].(map[string]any)
		if fieldRef["fieldPath"] != field {
			t.Fatalf("preloader %s fieldPath = %#v", name, fieldRef["fieldPath"])
		}
	}
	mounts := preloader["volumeMounts"].([]any)
	wantMounts := map[string]bool{"platform-data-input": false, "local-cache-data1": false, "local-cache-data2": false}
	for _, item := range mounts {
		mount := item.(map[string]any)
		name, tracked := mount["name"].(string)
		if tracked {
			if _, expected := wantMounts[name]; !expected {
				t.Fatalf("preloader received an unrelated mount %q: %#v", name, mounts)
			}
			wantMounts[name] = true
		}
	}
	for name, found := range wantMounts {
		if !found {
			t.Fatalf("preloader is missing %s: %#v", name, mounts)
		}
	}

	head := cacheHeadPodSpec(t, manifest.Object)
	if init, ok := head["initContainers"].([]any); ok && len(init) != 0 {
		t.Fatalf("head must not duplicate the worker dataset: %#v", init)
	}
	if podEnvironment(head)["PLATFORM_DATASET_PATH"] != domain.DataMountInputPath {
		t.Fatalf("head keeps the durable selected input mount: %#v", podEnvironment(head))
	}
	if strings.Contains(string(mustJSON(cacheSubmitterPodSpec(t, manifest.Object))), "dataset-cache-preloader") {
		t.Fatal("submitter must never reserve or preload worker cache")
	}
}

func environmentValues(container map[string]any) map[string]string {
	values := map[string]string{}
	for _, item := range container["env"].([]any) {
		entry := item.(map[string]any)
		name, _ := entry["name"].(string)
		value, _ := entry["value"].(string)
		values[name] = value
	}
	return values
}

func TestRenderRayJobUsesConfiguredSpellingForEquivalentCacheSize(t *testing.T) {
	job := validRenderJob()
	job.Spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "512000Mi"}
	manifest, err := RenderRayJob(job, runtimeCacheRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}

	assertGenericEphemeralCache(t, "head", cacheHeadPodSpec(t, manifest.Object), map[string]cacheVolumeExpectation{
		"local-cache-data1": {storageClass: "ray-cache-local-data1", size: "250Gi", mountPath: "/mnt/cache"},
	})
	assertGenericEphemeralCache(t, "worker", cacheWorkerPodSpec(t, manifest.Object), map[string]cacheVolumeExpectation{
		"local-cache-data1": {storageClass: "ray-cache-local-data1", size: "250Gi", mountPath: "/mnt/cache"},
		"local-cache-data2": {storageClass: "ray-cache-local-data2", size: "250Gi", mountPath: "/mnt/cache2"},
	})
}

func TestRenderRayJobCapabilityEnabledDoesNotMountCacheForOmittedRequest(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), runtimeCacheRenderOptions())
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

func TestRenderRayJobRejectsRuntimeCacheWhenCapabilityDisabled(t *testing.T) {
	job := validRenderJob()
	job.Spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "200Gi"}
	if _, err := RenderRayJob(job, testRenderOptions()); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled runtime cache rejection, got %v", err)
	}
}

func TestRenderRayJobRejectsRuntimeCacheSizeOutsidePolicy(t *testing.T) {
	job := validRenderJob()
	job.Spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "300Gi"}
	if _, err := RenderRayJob(job, runtimeCacheRenderOptions()); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected disallowed runtime cache rejection, got %v", err)
	}
}

func TestRenderRayJobRejectsCacheMountedInsideRayDefaultTempDirectory(t *testing.T) {
	options := testRenderOptions()
	options.LocalCache = LocalCacheOptions{
		Enabled:           true,
		StorageClassData1: "ray-cache-local-data1",
		StorageClassData2: "ray-cache-local-data2",
		AllowedSizes:      []string{"200Gi"},
		DefaultSize:       "200Gi",
		MaxSize:           "5Ti",
		MountPathData1:    "/tmp/ray",
		MountPathData2:    "/mnt/cache2",
	}
	if _, err := RenderRayJob(validRenderJob(), options); err == nil || !strings.Contains(err.Error(), "mount path") {
		t.Fatalf("expected unsafe Ray temp-directory cache mount to be rejected, got %v", err)
	}
}

func runtimeCacheRenderOptions() RenderOptions {
	options := testRenderOptions()
	options.LocalCache = LocalCacheOptions{
		Enabled:           true,
		StorageClassData1: "ray-cache-local-data1",
		StorageClassData2: "ray-cache-local-data2",
		AllowedSizes:      []string{"200Gi", "500Gi", "1Ti", "2Ti", "4Ti", "5Ti"},
		DefaultSize:       "200Gi",
		MaxSize:           "5Ti",
		MountPathData1:    "/mnt/cache",
		MountPathData2:    "/mnt/cache2",
	}
	return options
}

type cacheVolumeExpectation struct {
	storageClass string
	size         string
	mountPath    string
}

func assertGenericEphemeralCache(t *testing.T, podName string, podSpec map[string]any, expected map[string]cacheVolumeExpectation) {
	t.Helper()
	volumes, _, _ := nestedSlice(podSpec, "volumes")
	foundVolumes := map[string]bool{}
	for _, item := range volumes {
		volume := item.(map[string]any)
		name, _ := volume["name"].(string)
		want, tracked := expected[name]
		if !tracked {
			continue
		}
		ephemeral, ok := volume["ephemeral"].(map[string]any)
		if !ok {
			t.Fatalf("%s cache must use a generic ephemeral volume: %#v", podName, volume)
		}
		claim := ephemeral["volumeClaimTemplate"].(map[string]any)
		spec := claim["spec"].(map[string]any)
		requests := spec["resources"].(map[string]any)["requests"].(map[string]any)
		if spec["storageClassName"] == want.storageClass && requests["storage"] == want.size {
			foundVolumes[name] = true
		}
	}
	for name := range expected {
		if !foundVolumes[name] {
			t.Fatalf("%s generic ephemeral cache volume %s missing or malformed: %#v", podName, name, volumes)
		}
	}

	containers, _, _ := nestedSlice(podSpec, "containers")
	mounts := containers[0].(map[string]any)["volumeMounts"].([]any)
	paths := map[string]string{}
	for _, item := range mounts {
		mount := item.(map[string]any)
		if name, tracked := mount["name"].(string); tracked {
			paths[name] = mount["mountPath"].(string)
		}
	}
	for name, want := range expected {
		if paths[name] != want.mountPath {
			t.Fatalf("%s cache %s must be mounted at %s: %#v", podName, name, want.mountPath, mounts)
		}
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
