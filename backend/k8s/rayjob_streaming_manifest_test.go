package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

const (
	streamingDatasetID        = "dataset-labeled-full"
	streamingDatasetVersionID = "version-20260830"
	streamingManifestDigest   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// The selected PVC is already rooted at ray-train/, so Kubernetes subPath
	// must be relative to that claim root rather than repeat the bucket prefix.
	streamingDatasetRootSubPath       = "platform/datasets/dataset-labeled-full"
	streamingDatasetMountPath         = "/mnt/data/.platform/datasets/dataset-labeled-full"
	streamingManifestPath             = "/mnt/data/.platform/datasets/dataset-labeled-full/manifests/version-20260830.parquet"
	streamingShardPath                = "/mnt/data/.platform/datasets/dataset-labeled-full/shards/sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.parquet"
	streamingTrainSamples       int64 = 15228
)

func streamingManifestJob() domain.TrainingJob {
	job := managedRenderJob(domain.RayVersionCanary)
	job.Spec.DataMode = domain.DataModeStreaming
	job.Spec.DatasetRef = domain.DatasetReference{
		Dataset: streamingDatasetID,
		Version: streamingDatasetVersionID,
	}
	job.Spec.CachePolicy = domain.DatasetCachePolicyAuto
	job.DatasetProvenance = domain.DatasetProvenance{
		DatasetID:        streamingDatasetID,
		DatasetVersionID: streamingDatasetVersionID,
		ManifestSHA256:   streamingManifestDigest,
		DataMode:         domain.DataModeStreaming,
		CachePolicy:      domain.DatasetCachePolicyAuto,
	}
	return job
}

func streamingManifestMount() DatasetManifestMount {
	return DatasetManifestMount{
		DatasetID:          streamingDatasetID,
		DatasetVersionID:   streamingDatasetVersionID,
		ManifestSHA256:     streamingManifestDigest,
		TrainSamples:       streamingTrainSamples,
		ClaimName:          "data-tenant-local",
		DatasetRootSubPath: streamingDatasetRootSubPath,
	}
}

func streamingRenderOptions() RenderOptions {
	options := testRenderOptions()
	mount := streamingManifestMount()
	options.DatasetManifest = &mount
	return options
}

func TestRenderStreamingJobRequiresResolvedImmutableManifestMount(t *testing.T) {
	manifest, err := RenderRayJob(streamingManifestJob(), testRenderOptions())
	if err == nil || !strings.Contains(err.Error(), "resolved dataset root") {
		t.Fatalf("streaming job without a resolved manifest returned manifest=%#v err=%v", manifest, err)
	}
	if manifest != nil {
		t.Fatalf("streaming job without an immutable manifest rendered a workload: %#v", manifest)
	}
}

func TestRenderStreamingJobMountsOnlyExactDatasetRootReadOnlyOnHeadAndWorkers(t *testing.T) {
	job := streamingManifestJob()
	manifest, err := RenderRayJob(job, streamingRenderOptions())
	if err != nil {
		t.Fatalf("render streaming RayJob: %v", err)
	}

	head := cacheHeadPodSpec(t, manifest.Object)
	worker := cacheWorkerPodSpec(t, manifest.Object)
	for name, pod := range map[string]map[string]any{"head": head, "worker": worker} {
		assertStreamingDatasetRootMount(t, name, pod)
		environment := podEnvironment(pod)
		for key, expected := range streamingManifestEnvironment(job) {
			if environment[key] != expected {
				t.Fatalf("%s %s=%q, want %q (all env: %#v)", name, key, environment[key], expected, environment)
			}
		}
		for _, forbidden := range []string{
			"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
			"TOS_ACCESS_KEY", "TOS_SECRET_KEY", "TOS_ENDPOINT", "TOS_BUCKET",
		} {
			if _, exists := environment[forbidden]; exists {
				t.Fatalf("%s received object-store credential %s: %#v", name, forbidden, environment)
			}
		}
	}

	spec, _, _ := unstructured.NestedMap(manifest.Object, "spec")
	runtimeEnvironment, _ := spec["runtimeEnvYAML"].(string)
	for key, expected := range streamingManifestEnvironment(job) {
		line := key + ": " + strconvQuote(expected)
		if !strings.Contains(runtimeEnvironment, line) {
			t.Fatalf("managed driver runtime environment is missing %q: %q", line, runtimeEnvironment)
		}
	}
	for _, forbidden := range []string{"ray-train/platform/datasets", "data-tenant-local", "tos://", "TOS_ACCESS_KEY", "TOS_SECRET_KEY"} {
		if strings.Contains(runtimeEnvironment, forbidden) {
			t.Fatalf("runtimeEnvYAML exposed private storage detail %q: %q", forbidden, runtimeEnvironment)
		}
	}

	submitter, found, err := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if err != nil || !found {
		t.Fatalf("read submitter Pod: found=%v err=%v", found, err)
	}
	submitterJSON := renderJSON(t, submitter)
	if podHasMountPath(submitter, streamingDatasetMountPath) ||
		strings.Contains(submitterJSON, DatasetRootContainerPath) ||
		strings.Contains(submitterJSON, streamingDatasetRootSubPath) ||
		strings.Contains(submitterJSON, "PLATFORM_DATASET_") {
		t.Fatalf("submitter must not mount the private dataset root: %#v", submitter)
	}

	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	publicMetadata := renderJSON(t, map[string]any{
		"labels":      manifest.GetLabels(),
		"annotations": manifest.GetAnnotations(),
		"entrypoint":  entrypoint,
	})
	for _, forbidden := range []string{streamingDatasetRootSubPath, streamingManifestDigest, "tos://", "TOS_ACCESS_KEY", "TOS_SECRET_KEY"} {
		if strings.Contains(publicMetadata, forbidden) {
			t.Fatalf("public workload surface exposed private dataset detail %q: %s", forbidden, publicMetadata)
		}
	}
}

func TestRenderStreamingBoundedCacheUsesDualNVMeOnWorkersAndOneSpillVolumeOnHead(t *testing.T) {
	job := streamingManifestJob()
	job.Spec.CachePolicy = domain.DatasetCachePolicyBounded
	job.DatasetProvenance.CachePolicy = domain.DatasetCachePolicyBounded
	options := runtimeCacheRenderOptions()
	mount := streamingManifestMount()
	options.DatasetManifest = &mount

	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render bounded streaming job: %v", err)
	}
	worker := cacheWorkerPodSpec(t, manifest.Object)
	assertGenericEphemeralCache(t, "worker", worker, map[string]cacheVolumeExpectation{
		"local-cache-data1": {storageClass: "ray-cache-local-data1", size: "100Gi", mountPath: "/mnt/cache"},
		"local-cache-data2": {storageClass: "ray-cache-local-data2", size: "100Gi", mountPath: "/mnt/cache2"},
	})
	if got := podEnvironment(worker)["PLATFORM_CACHE_PATHS"]; got != "/mnt/cache:/mnt/cache2" {
		t.Fatalf("bounded worker cache paths=%q", got)
	}
	if got := podEnvironment(worker)["PLATFORM_DATASET_CACHE_POLICY"]; got != "bounded" {
		t.Fatalf("bounded worker policy=%q", got)
	}
	head := cacheHeadPodSpec(t, manifest.Object)
	assertGenericEphemeralCache(t, "head", head, map[string]cacheVolumeExpectation{
		"local-cache-data1": {storageClass: "ray-cache-local-data1", size: "100Gi", mountPath: "/mnt/cache"},
	})
	if got := podEnvironment(head)["PLATFORM_CACHE_PATHS"]; got != "/mnt/cache" {
		t.Fatalf("bounded head cache paths=%q", got)
	}
	if strings.Contains(string(mustJSON(head)), "local-cache-data2") || strings.Contains(podEnvironment(head)["RAY_object_spilling_config"], "/mnt/cache2") {
		t.Fatalf("streaming head must not reserve the worker data2 cache: %s", mustJSON(head))
	}
	submitter := cacheSubmitterPodSpec(t, manifest.Object)
	if strings.Contains(string(mustJSON(submitter)), "local-cache") || podEnvironment(submitter)["PLATFORM_CACHE_PATHS"] != "" {
		t.Fatalf("submitter received streaming cache: %s", mustJSON(submitter))
	}
}

func TestRenderStreamingBoundedCacheFailsBeforeWorkloadWhenCapabilityIsUnavailable(t *testing.T) {
	job := streamingManifestJob()
	job.Spec.CachePolicy = domain.DatasetCachePolicyBounded
	job.DatasetProvenance.CachePolicy = domain.DatasetCachePolicyBounded
	manifest, err := RenderRayJob(job, streamingRenderOptions())
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("bounded cache without capability returned manifest=%#v err=%v", manifest, err)
	}
	if manifest != nil {
		t.Fatalf("bounded cache failure rendered a workload: %#v", manifest)
	}
}

func TestRenderStreamingAutoCacheFallsBackWhenCapabilityIsUnavailable(t *testing.T) {
	manifest, err := RenderRayJob(streamingManifestJob(), streamingRenderOptions())
	if err != nil {
		t.Fatalf("auto cache must permit source fallback: %v", err)
	}
	worker := cacheWorkerPodSpec(t, manifest.Object)
	if strings.Contains(string(mustJSON(worker)), "local-cache") || podEnvironment(worker)["PLATFORM_CACHE_PATHS"] != "" {
		t.Fatalf("auto fallback unexpectedly mounted cache: %s", mustJSON(worker))
	}
}

func TestRenderStreamingJobRejectsManifestIdentityMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DatasetManifestMount)
	}{
		{name: "dataset", mutate: func(value *DatasetManifestMount) { value.DatasetID = "dataset-other" }},
		{name: "version", mutate: func(value *DatasetManifestMount) { value.DatasetVersionID = "version-20260831" }},
		{name: "digest", mutate: func(value *DatasetManifestMount) { value.ManifestSHA256 = strings.Repeat("c", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testRenderOptions()
			mount := streamingManifestMount()
			test.mutate(&mount)
			options.DatasetManifest = &mount
			manifest, err := RenderRayJob(streamingManifestJob(), options)
			if err == nil || !strings.Contains(err.Error(), "does not match immutable provenance") {
				t.Fatalf("mismatched %s returned manifest=%#v err=%v", test.name, manifest, err)
			}
			if manifest != nil {
				t.Fatalf("mismatched %s rendered a workload: %#v", test.name, manifest)
			}
		})
	}
}

func TestRenderStreamingJobRejectsSpecThatDiffersFromPersistedProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.TrainingJob)
	}{
		{name: "dataset", mutate: func(job *domain.TrainingJob) { job.Spec.DatasetRef.Dataset = "dataset-other" }},
		{name: "version", mutate: func(job *domain.TrainingJob) { job.Spec.DatasetRef.Version = "version-20260831" }},
		{name: "cache policy", mutate: func(job *domain.TrainingJob) { job.Spec.CachePolicy = domain.DatasetCachePolicyOff }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := streamingManifestJob()
			test.mutate(&job)
			manifest, err := RenderRayJob(job, streamingRenderOptions())
			if err == nil || !strings.Contains(err.Error(), "does not match immutable dataset provenance") {
				t.Fatalf("mismatched %s returned manifest=%#v err=%v", test.name, manifest, err)
			}
			if manifest != nil {
				t.Fatalf("mismatched %s rendered a workload: %#v", test.name, manifest)
			}
		})
	}
}

func TestRenderStreamingJobRejectsUnsafeManifestPVCMount(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DatasetManifestMount)
	}{
		{name: "empty claim", mutate: func(value *DatasetManifestMount) { value.ClaimName = "" }},
		{name: "invalid claim", mutate: func(value *DatasetManifestMount) { value.ClaimName = "INVALID_CLAIM" }},
		{name: "absolute subpath", mutate: func(value *DatasetManifestMount) { value.DatasetRootSubPath = "/" + streamingDatasetRootSubPath }},
		{name: "traversal", mutate: func(value *DatasetManifestMount) {
			value.DatasetRootSubPath = "ray-train/platform/../private/dataset-labeled-full"
		}},
		{name: "leading traversal", mutate: func(value *DatasetManifestMount) {
			value.DatasetRootSubPath = "../" + streamingDatasetRootSubPath
		}},
		{name: "encoded traversal", mutate: func(value *DatasetManifestMount) {
			value.DatasetRootSubPath = "ray-train/platform/%2e%2e/private/dataset-labeled-full"
		}},
		{name: "wrong dataset root", mutate: func(value *DatasetManifestMount) {
			value.DatasetRootSubPath = "ray-train/platform/datasets/other"
		}},
		{name: "dataset id is only a prefix", mutate: func(value *DatasetManifestMount) {
			value.DatasetRootSubPath = streamingDatasetRootSubPath + "-other"
		}},
		{name: "control character", mutate: func(value *DatasetManifestMount) { value.DatasetRootSubPath = streamingDatasetRootSubPath + "\nsecret" }},
		{name: "embedded control character", mutate: func(value *DatasetManifestMount) {
			value.DatasetRootSubPath = "ray-train/\x1b/platform/datasets/" + streamingDatasetID
		}},
		{name: "missing train samples", mutate: func(value *DatasetManifestMount) { value.TrainSamples = 0 }},
		{name: "negative train samples", mutate: func(value *DatasetManifestMount) { value.TrainSamples = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testRenderOptions()
			mount := streamingManifestMount()
			test.mutate(&mount)
			options.DatasetManifest = &mount
			manifest, err := RenderRayJob(streamingManifestJob(), options)
			if err == nil {
				t.Fatalf("unsafe manifest mount rendered workload: %#v", manifest)
			}
			if manifest != nil {
				t.Fatalf("unsafe manifest mount returned workload: %#v", manifest)
			}
		})
	}
}

func TestNonStreamingRayJobIgnoresDatasetManifestRenderOptionByteForByte(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	baseline, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	options := testRenderOptions()
	mount := streamingManifestMount()
	options.DatasetManifest = &mount
	withIgnoredOption, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline.Object, withIgnoredOption.Object) {
		t.Fatalf("non-streaming renderer semantics changed:\nbaseline=%s\nrendered=%s", renderJSON(t, baseline.Object), renderJSON(t, withIgnoredOption.Object))
	}
}

type recordingDatasetManifestResolver struct {
	request DatasetManifestResolutionRequest
	mount   DatasetManifestMount
	err     error
	calls   int
}

func (resolver *recordingDatasetManifestResolver) ResolveDatasetManifestMount(_ context.Context, request DatasetManifestResolutionRequest) (DatasetManifestMount, error) {
	resolver.calls++
	resolver.request = request
	return resolver.mount, resolver.err
}

func TestReconcilerResolvesPrivateManifestFromPersistedProvenance(t *testing.T) {
	job := streamingManifestJob()
	resolver := &recordingDatasetManifestResolver{mount: streamingManifestMount()}
	reconciler := NewReconciler(nil, nil, testRenderOptions()).WithDatasetManifestResolver(resolver)

	options, err := reconciler.renderOptionsForJob(context.Background(), job)
	if err != nil {
		t.Fatalf("resolve render options: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d, want 1", resolver.calls)
	}
	wantRequest := DatasetManifestResolutionRequest{
		TenantID:         job.TenantID,
		DatasetID:        streamingDatasetID,
		DatasetVersionID: streamingDatasetVersionID,
		ManifestSHA256:   streamingManifestDigest,
	}
	if resolver.request != wantRequest {
		t.Fatalf("resolver request=%+v, want %+v", resolver.request, wantRequest)
	}
	if options.DatasetManifest == nil || *options.DatasetManifest != streamingManifestMount() {
		t.Fatalf("resolved render options lost immutable manifest: %+v", options.DatasetManifest)
	}
	if _, err := RenderRayJob(job, options); err != nil {
		t.Fatalf("resolved options did not render: %v", err)
	}
}

func TestReconcilerFailsClosedWhenStreamingManifestCannotBeResolved(t *testing.T) {
	job := streamingManifestJob()
	for _, test := range []struct {
		name       string
		reconciler *Reconciler
		want       string
	}{
		{name: "resolver missing", reconciler: NewReconciler(nil, nil, testRenderOptions()), want: "resolver is not configured"},
		{name: "repository failure", reconciler: NewReconciler(nil, nil, testRenderOptions()).WithDatasetManifestResolver(&recordingDatasetManifestResolver{err: errors.New("catalog unavailable")}), want: "catalog unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, err := test.reconciler.renderOptionsForJob(context.Background(), job)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("render options=%+v err=%v, want error containing %q", options, err, test.want)
			}
		})
	}
}

func TestReconcilerDoesNotResolveManifestForLegacyJobs(t *testing.T) {
	resolver := &recordingDatasetManifestResolver{err: errors.New("must not be called")}
	reconciler := NewReconciler(nil, nil, testRenderOptions()).WithDatasetManifestResolver(resolver)
	options, err := reconciler.renderOptionsForJob(context.Background(), validRenderJob())
	if err != nil {
		t.Fatalf("legacy render options: %v", err)
	}
	if resolver.calls != 0 || options.DatasetManifest != nil {
		t.Fatalf("legacy job resolved a private manifest: calls=%d options=%+v", resolver.calls, options.DatasetManifest)
	}
}

func assertStreamingDatasetRootMount(t *testing.T, podName string, pod map[string]any) {
	t.Helper()
	volumes, _ := pod["volumes"].([]any)
	foundVolume := false
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] != datasetRootVolumeName {
			continue
		}
		pvc, _ := volume["persistentVolumeClaim"].(map[string]any)
		if pvc["claimName"] != "data-tenant-local" || pvc["readOnly"] != true {
			t.Fatalf("%s manifest PVC is not exact/read-only: %#v", podName, volume)
		}
		foundVolume = true
	}
	if !foundVolume {
		t.Fatalf("%s is missing the manifest PVC: %#v", podName, volumes)
	}

	containers, _ := pod["containers"].([]any)
	mounts, _ := containers[0].(map[string]any)["volumeMounts"].([]any)
	foundMount := false
	for _, raw := range mounts {
		mount, _ := raw.(map[string]any)
		if mount["name"] != datasetRootVolumeName {
			continue
		}
		if mount["mountPath"] != streamingDatasetMountPath || mount["subPath"] != streamingDatasetRootSubPath || mount["readOnly"] != true {
			t.Fatalf("%s dataset root mount is not exact/read-only: %#v", podName, mount)
		}
		if _, dynamic := mount["subPathExpr"]; dynamic {
			t.Fatalf("%s manifest mount must not use subPathExpr: %#v", podName, mount)
		}
		foundMount = true
	}
	if !foundMount {
		t.Fatalf("%s is missing the dataset root mount: %#v", podName, mounts)
	}

	// The same confined dataset directory must expose both the immutable
	// manifest and every content-addressed shard referenced by it. Mounting only
	// the manifest file would make this reachability contract impossible.
	for _, reachable := range []string{streamingManifestPath, streamingShardPath} {
		prefix := streamingDatasetMountPath + "/"
		if !strings.HasPrefix(reachable, prefix) || strings.Contains(strings.TrimPrefix(reachable, prefix), "../") {
			t.Fatalf("%s is not reachable below the confined dataset mount %s", reachable, streamingDatasetMountPath)
		}
	}
}

func streamingManifestEnvironment(job domain.TrainingJob) map[string]string {
	return map[string]string{
		"PLATFORM_DATASET_ID":               job.DatasetProvenance.DatasetID,
		"PLATFORM_DATASET_VERSION_ID":       job.DatasetProvenance.DatasetVersionID,
		"PLATFORM_DATASET_MANIFEST_SHA256":  job.DatasetProvenance.ManifestSHA256,
		"PLATFORM_DATASET_MANIFEST_PATH":    streamingManifestPath,
		"PLATFORM_DATASET_ROOT":             DatasetRootContainerPath,
		"PLATFORM_DATASET_TRAIN_SAMPLES":    "15228",
		"PLATFORM_DATASET_CACHE_POLICY":     string(job.DatasetProvenance.CachePolicy),
		"RAYTRAIN_DATASET_PREFETCH_BATCHES": "2",
		"RAYTRAIN_DATASET_SHUFFLE_SEED":     "0",
	}
}

func renderJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode rendered value: %v", err)
	}
	return string(payload)
}

func strconvQuote(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}
