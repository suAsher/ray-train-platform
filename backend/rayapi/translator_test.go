package rayapi

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

const (
	testPackageSHA256  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRayPackageSHA1 = "0123456789abcdef"
	testImageDigest    = "registry.example/ray@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
)

func TestParsePackageNameAcceptsOnlyCanonicalGCSZipNames(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		value    string
		want     string
	}{
		{name: "sha256 package", protocol: "gcs", value: testPackageSHA256 + ".zip", want: testPackageSHA256 + ".zip"},
		{name: "ray 2.35 sha1 package", protocol: "gcs", value: "_ray_pkg_" + testRayPackageSHA1 + ".zip", want: "_ray_pkg_" + testRayPackageSHA1 + ".zip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParsePackageName(test.protocol, test.value)
			if err != nil {
				t.Fatalf("parse package name: %v", err)
			}
			if parsed.Name != test.want {
				t.Fatalf("name=%q, want %q", parsed.Name, test.want)
			}
		})
	}
}

func TestParsePackageNameRejectsUnsafeOrUnsupportedNames(t *testing.T) {
	tests := []struct {
		protocol string
		value    string
	}{
		{protocol: "s3", value: testPackageSHA256 + ".zip"},
		{protocol: "https", value: testPackageSHA256 + ".zip"},
		{protocol: "gcs", value: "../x.zip"},
		{protocol: "gcs", value: "x/y.zip"},
		{protocol: "gcs", value: "working_dir.zip"},
		{protocol: "gcs", value: testPackageSHA256 + ".tar.gz"},
		{protocol: "gcs", value: strings.ToUpper(testPackageSHA256) + ".zip"},
		{protocol: "gcs", value: "_ray_pkg_" + strings.ToUpper(testRayPackageSHA1) + ".zip"},
		{protocol: "gcs", value: "_ray_pkg_" + testPackageSHA256 + ".zip"},
		{protocol: "gcs", value: testPackageSHA256[:63] + ".zip"},
	}
	for _, test := range tests {
		if _, err := ParsePackageName(test.protocol, test.value); err == nil {
			t.Fatalf("accepted protocol=%q package=%q", test.protocol, test.value)
		}
	}
}

func TestTranslateSubmitRequestUsesOnlyValidatedPlatformMetadata(t *testing.T) {
	request := JobSubmitRequest{
		Entrypoint:   "python train.py --epochs 3",
		SubmissionID: "raysubmit_example",
		RuntimeEnv:   map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
		Metadata: map[string]string{
			"ray-platform.image":             testImageDigest,
			"ray-platform.worker-replicas":   "2",
			"ray-platform.gpus-per-worker":   "1",
			"ray-platform.cpu-per-worker":    "8",
			"ray-platform.memory-per-worker": "32Gi",
			"ray-platform.queue":             "tenant-a-gpu",
		},
	}

	translated, err := TranslateSubmitRequest(request)
	if err != nil {
		t.Fatalf("translate submit request: %v", err)
	}
	if translated.Package.Name != testPackageSHA256+".zip" || translated.ExternalSubmissionID != "raysubmit_example" {
		t.Fatalf("unexpected transport translation: %+v", translated)
	}
	if translated.Spec.Image != testImageDigest || translated.Spec.Resources.WorkerReplicas != 2 || translated.Spec.Resources.GPUsPerWorker != 1 || translated.Spec.Resources.CPUPerWorker != 8 || translated.Spec.Resources.MemoryPerWorker != "32Gi" || translated.Spec.Queue != "tenant-a-gpu" {
		t.Fatalf("metadata was not translated into the platform job spec: %+v", translated.Spec)
	}
	if translated.Spec.Execution.Mode != domain.ExecutionModeRayTrain {
		t.Fatalf("two-worker Ray CLI job must use ray_train, got %q", translated.Spec.Execution.Mode)
	}
	if len(translated.Spec.Entrypoint.Command) != 3 || translated.Spec.Entrypoint.Command[0] != "/bin/sh" || translated.Spec.Entrypoint.Command[1] != "-lc" || translated.Spec.Entrypoint.Command[2] != request.Entrypoint {
		t.Fatalf("entrypoint was not preserved as a shell command: %+v", translated.Spec.Entrypoint)
	}
}

func TestTranslateSubmitRequestWithDefaultsAcceptsBareRayCLIWorkingDirectory(t *testing.T) {
	defaults := SubmissionDefaults{
		Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1,
		CPUPerWorker: 8, MemoryPerWorker: "32Gi",
	}
	translated, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
		Entrypoint:   "python train.py --epochs 3",
		SubmissionID: "bare_ray_cli",
		RuntimeEnv:   map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
	}, defaults)
	if err != nil {
		t.Fatalf("translate bare Ray CLI request: %v", err)
	}
	if translated.Spec.Image != testImageDigest || translated.Spec.Resources.WorkerReplicas != 1 || translated.Spec.Resources.GPUsPerWorker != 1 || translated.Spec.Resources.CPUPerWorker != 8 || translated.Spec.Resources.MemoryPerWorker != "32Gi" {
		t.Fatalf("bare Ray CLI request did not use safe platform defaults: %+v", translated.Spec)
	}
	if translated.Spec.Queue != "" {
		t.Fatalf("tenant queue must be selected by the submission service, got %q", translated.Spec.Queue)
	}
	if translated.Spec.Execution.Mode != domain.ExecutionModeSingleGPU {
		t.Fatalf("default one-GPU Ray CLI job must use single_gpu, got %q", translated.Spec.Execution.Mode)
	}
	if translated.Spec.Cache != (domain.CacheRequest{}) {
		t.Fatalf("omitted cache metadata must leave cache off, got %+v", translated.Spec.Cache)
	}
}

func TestTranslateSubmitRequestWithDefaultsTreatsExplicitCacheOffAsZero(t *testing.T) {
	translated, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
		Entrypoint: "python train.py", RuntimeEnv: map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
		Metadata: map[string]string{metadataCacheMode: "off"},
	}, SubmissionDefaults{Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"})
	if err != nil {
		t.Fatalf("translate explicit cache off: %v", err)
	}
	if translated.Spec.Cache != (domain.CacheRequest{}) {
		t.Fatalf("explicit cache off must leave zero cache, got %+v", translated.Spec.Cache)
	}
}

func TestTranslateSubmitRequestWithDefaultsAcceptsCacheOnlyMetadata(t *testing.T) {
	defaults := SubmissionDefaults{
		Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1,
		CPUPerWorker: 8, MemoryPerWorker: "32Gi",
	}
	translated, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
		Entrypoint:   "python train.py",
		SubmissionID: "cache_only",
		RuntimeEnv:   map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
		Metadata: map[string]string{
			"platform.cache.mode": "runtime",
			"platform.cache.size": "200Gi",
			"ray.user-note":       "ignored as before",
		},
	}, defaults)
	if err != nil {
		t.Fatalf("translate cache-only metadata: %v", err)
	}
	if translated.Spec.Image != defaults.Image || translated.Spec.Resources.WorkerReplicas != 1 || translated.Spec.Resources.GPUsPerWorker != 1 {
		t.Fatalf("cache-only metadata must retain operator defaults: %+v", translated.Spec)
	}
	if translated.Spec.Cache.Mode != domain.CacheModeRuntime || translated.Spec.Cache.Size != "200Gi" {
		t.Fatalf("cache metadata was not preserved: %+v", translated.Spec.Cache)
	}
}

func TestTranslateSubmitRequestCombinesExplicitResourcesWithOptionalRuntimeCacheSize(t *testing.T) {
	metadata := map[string]string{
		metadataImage: testImageDigest, metadataWorkerReplicas: "2", metadataGPUsPerWorker: "1",
		metadataCPUPerWorker: "8", metadataMemoryWorker: "32Gi", metadataQueue: "tenant-a-gpu",
		metadataCacheMode: "runtime",
	}
	translated, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
		Entrypoint: "python train.py", RuntimeEnv: map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"}, Metadata: metadata,
	}, SubmissionDefaults{Image: "unused"})
	if err != nil {
		t.Fatalf("translate combined metadata: %v", err)
	}
	if translated.Spec.Resources.WorkerReplicas != 2 || translated.Spec.Image != testImageDigest || translated.Spec.Queue != "tenant-a-gpu" {
		t.Fatalf("explicit resource metadata was not preserved: %+v", translated.Spec)
	}
	if translated.Spec.Cache.Mode != domain.CacheModeRuntime || translated.Spec.Cache.Size != "" {
		t.Fatalf("runtime cache without size must reach SubmissionService normalization: %+v", translated.Spec.Cache)
	}
}

func TestTranslateSubmitRequestRejectsInvalidCacheMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
	}{
		{name: "unknown cache key", metadata: map[string]string{"platform.cache.storage-class": "fast"}},
		{name: "mount path is not exposed", metadata: map[string]string{"platform.cache.mount-path": "/cache"}},
		{name: "unsupported mode", metadata: map[string]string{metadataCacheMode: "durable"}},
		{name: "off with size", metadata: map[string]string{metadataCacheMode: "off", metadataCacheSize: "100Gi"}},
		{name: "omitted mode with size", metadata: map[string]string{metadataCacheSize: "100Gi"}},
	}
	defaults := SubmissionDefaults{Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
				Entrypoint: "python train.py", RuntimeEnv: map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"}, Metadata: test.metadata,
			}, defaults)
			if err == nil {
				t.Fatal("invalid cache metadata was accepted")
			}
		})
	}
}

func TestTranslateSubmitRequestInfersExecutionModeFromResourceShape(t *testing.T) {
	tests := []struct {
		name    string
		workers string
		gpus    string
		want    domain.ExecutionMode
	}{
		{name: "single GPU", workers: "1", gpus: "1", want: domain.ExecutionModeSingleGPU},
		{name: "single node DDP", workers: "1", gpus: "8", want: domain.ExecutionModeTorchrun},
		{name: "multi node DDP", workers: "2", gpus: "8", want: domain.ExecutionModeRayTrain},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translated, err := TranslateSubmitRequest(JobSubmitRequest{
				Entrypoint: "python train.py",
				RuntimeEnv: map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
				Metadata: map[string]string{
					metadataImage: testImageDigest, metadataWorkerReplicas: test.workers,
					metadataGPUsPerWorker: test.gpus, metadataCPUPerWorker: "32",
					metadataMemoryWorker: "128Gi", metadataQueue: "tenant-a-gpu",
				},
			})
			if err != nil {
				t.Fatalf("translate request: %v", err)
			}
			if translated.Spec.Execution.Mode != test.want {
				t.Fatalf("execution mode=%q, want %q", translated.Spec.Execution.Mode, test.want)
			}
		})
	}
}

func TestTranslateSubmitRequestWithDefaultsRejectsPartialPlatformMetadata(t *testing.T) {
	_, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
		Entrypoint: "python train.py",
		RuntimeEnv: map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
		Metadata:   map[string]string{metadataWorkerReplicas: "2"},
	}, SubmissionDefaults{Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"})
	if err == nil {
		t.Fatal("partial platform metadata must not silently combine with defaults")
	}
}

func TestTranslateSubmitRequestWithDefaultsRejectsUnknownReservedMetadata(t *testing.T) {
	_, err := TranslateSubmitRequestWithDefaults(JobSubmitRequest{
		Entrypoint: "python train.py",
		RuntimeEnv: map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"},
		Metadata:   map[string]string{"ray-platform.gpu-per-worker": "8"},
	}, SubmissionDefaults{Image: testImageDigest, WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"})
	if err == nil {
		t.Fatal("unknown reserved metadata must not silently fall back to defaults")
	}
}

func TestTranslateSubmitRequestRejectsMissingOrInvalidMetadata(t *testing.T) {
	valid := map[string]string{
		"ray-platform.image":             testImageDigest,
		"ray-platform.worker-replicas":   "1",
		"ray-platform.gpus-per-worker":   "1",
		"ray-platform.cpu-per-worker":    "8",
		"ray-platform.memory-per-worker": "32Gi",
		"ray-platform.queue":             "tenant-a-gpu",
	}
	tests := []struct {
		name       string
		mutate     func(map[string]string)
		runtimeEnv map[string]any
	}{
		{name: "missing image", mutate: func(metadata map[string]string) { delete(metadata, "ray-platform.image") }},
		{name: "image without tag or digest", mutate: func(metadata map[string]string) { metadata["ray-platform.image"] = "registry.example/ray" }},
		{name: "fractional replicas", mutate: func(metadata map[string]string) { metadata["ray-platform.worker-replicas"] = "1.5" }},
		{name: "too many GPUs", mutate: func(metadata map[string]string) { metadata["ray-platform.gpus-per-worker"] = "9" }},
		{name: "zero CPU", mutate: func(metadata map[string]string) { metadata["ray-platform.cpu-per-worker"] = "0" }},
		{name: "invalid memory", mutate: func(metadata map[string]string) { metadata["ray-platform.memory-per-worker"] = "1TB" }},
		{name: "empty queue", mutate: func(metadata map[string]string) { metadata["ray-platform.queue"] = "" }},
		{name: "missing working directory", mutate: func(map[string]string) {}, runtimeEnv: map[string]any{}},
		{name: "non gcs working directory", mutate: func(map[string]string) {}, runtimeEnv: map[string]any{"working_dir": "https://example.invalid/code.zip"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := make(map[string]string, len(valid))
			for key, value := range valid {
				metadata[key] = value
			}
			test.mutate(metadata)
			runtimeEnv := test.runtimeEnv
			if runtimeEnv == nil {
				runtimeEnv = map[string]any{"working_dir": "gcs://" + testPackageSHA256 + ".zip"}
			}
			_, err := TranslateSubmitRequest(JobSubmitRequest{Entrypoint: "python train.py", RuntimeEnv: runtimeEnv, Metadata: metadata})
			if err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}

func TestRayPackageArtifactIDIsDeterministicAndOwnerScoped(t *testing.T) {
	packageName := testPackageSHA256 + ".zip"
	first := rayPackageArtifactID("tenant-a", "user-a", packageName)
	if first == "" || first != rayPackageArtifactID("tenant-a", "user-a", packageName) {
		t.Fatalf("artifact id is not deterministic: %q", first)
	}
	if first == rayPackageArtifactID("tenant-b", "user-a", packageName) || first == rayPackageArtifactID("tenant-a", "user-b", packageName) || first == rayPackageArtifactID("tenant-a", "user-a", "_ray_pkg_"+testRayPackageSHA1+".zip") {
		t.Fatalf("artifact id is not owner/package scoped: %q", first)
	}
	if !strings.HasPrefix(first, "raypkg-") || len(first) != len("raypkg-")+64 {
		t.Fatalf("artifact id is not a safe hash identifier: %q", first)
	}
}
