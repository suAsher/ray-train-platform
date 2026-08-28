package k8s

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func managedRenderJob(version string) domain.TrainingJob {
	job := validRenderJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = version
	job.Spec.Managed = domain.ManagedTrainingPolicy{MaxFailures: 3}
	return job
}

func managedManifest(t *testing.T, job domain.TrainingJob) *unstructured.Unstructured {
	t.Helper()
	options := testRenderOptions()
	options.RayVersion = domain.RayVersionLegacy
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render managed RayJob: %v", err)
	}
	return manifest
}

func TestManagedJobUsesPerJobVersionAndManagedDriver(t *testing.T) {
	for _, version := range []string{domain.RayVersionProduction, domain.RayVersionCanary} {
		t.Run(version, func(t *testing.T) {
			manifest := managedManifest(t, managedRenderJob(version))
			gotVersion, _, _ := unstructured.NestedString(manifest.Object, "spec", "rayClusterSpec", "rayVersion")
			if gotVersion != version {
				t.Fatalf("Ray version must come from immutable job snapshot: got %q want %q", gotVersion, version)
			}
			entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
			want := "raytrain-managed --nodes 2 --gpus-per-node 8 --cpus-per-node 32 --max-failures 3 --checkpoint-every-epochs 0 --checkpoint-keep-latest 0 --checkpoint-keep-best 0 -- python train.py --epochs 3"
			if entrypoint != want {
				t.Fatalf("unexpected managed entrypoint:\n got: %q\nwant: %q", entrypoint, want)
			}
		})
	}
}

func TestManagedRayJobCarriesImmutableClusterAttemptIdentity(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.ClusterAttempt = 3
	job.RayJobName = job.ID + "-a3"
	manifest := managedManifest(t, job)
	const key = "raytrain.wellspiking.ai/cluster-attempt"
	if got := manifest.GetLabels()[key]; got != "3" {
		t.Fatalf("managed attempt label = %q, want 3", got)
	}
	if got := manifest.GetAnnotations()[key]; got != "3" {
		t.Fatalf("managed attempt annotation = %q, want 3", got)
	}

	legacy := validRenderJob()
	legacyManifest, err := RenderRayJob(legacy, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	if legacyManifest.GetLabels()[key] != "" || legacyManifest.GetAnnotations()[key] != "" {
		t.Fatal("legacy RayJob unexpectedly gained managed attempt identity")
	}
}

func TestUnadoptedManagedRayJobIsQuarantinedByCreationFence(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	options := testRenderOptions()
	options.managedCreationFence = 7
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	const fenceKey = "raytrain.wellspiking.ai/creation-fence"
	if got := manifest.GetLabels()[fenceKey]; got != "7" || manifest.GetAnnotations()[fenceKey] != "7" {
		t.Fatalf("creation fence was not immutable on the RayJob: labels=%v annotations=%v", manifest.GetLabels(), manifest.GetAnnotations())
	}
	if queue := manifest.GetLabels()["kueue.x-k8s.io/queue-name"]; queue != "" {
		t.Fatalf("unadopted managed RayJob was exposed to Kueue: %q", queue)
	}
	suspended, found, err := unstructured.NestedBool(manifest.Object, "spec", "suspend")
	if err != nil || !found || !suspended {
		t.Fatalf("unadopted managed RayJob is runnable: suspend=%v found=%v err=%v", suspended, found, err)
	}
}

func TestManagedInvalidEntrypointReturnsNoRayWorkload(t *testing.T) {
	tests := []domain.Entrypoint{
		{Command: []string{"torchrun", "train.py"}},
		{Command: []string{"bash", "train.sh"}},
		{Command: []string{"python", "../train.py"}},
		{Command: []string{"/bin/sh", "-lc", "python train.py && echo pwned"}},
	}
	for _, entrypoint := range tests {
		job := managedRenderJob(domain.RayVersionProduction)
		job.Spec.Entrypoint = entrypoint
		manifest, err := RenderRayJob(job, testRenderOptions())
		if err == nil || manifest != nil {
			t.Fatalf("invalid managed entrypoint returned a workload: entrypoint=%#v manifest=%#v err=%v", entrypoint, manifest, err)
		}
		if !strings.Contains(err.Error(), "managed entrypoint") {
			t.Fatalf("invalid managed entrypoint returned unclear error: %v", err)
		}
	}
}

func TestManagedRendererPreservesExplicitZeroPolicyBoundaries(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.Managed = domain.ManagedTrainingPolicy{}
	manifest := managedManifest(t, job)
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	want := "raytrain-managed --nodes 2 --gpus-per-node 8 --cpus-per-node 32 --max-failures 0 --checkpoint-every-epochs 0 --checkpoint-keep-latest 0 --checkpoint-keep-best 0 -- python train.py --epochs 3"
	if entrypoint != want {
		t.Fatalf("explicit zero policy boundaries were overwritten:\n got: %q\nwant: %q", entrypoint, want)
	}
}

func TestManagedEntrypointPropagatesPersistedCheckpointPolicyExactly(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.Managed = domain.ManagedTrainingPolicy{
		MaxFailures: 7,
		Checkpoint: domain.CheckpointPolicy{
			EveryEpochs: 4,
			KeepLatest:  9,
			KeepBest:    2,
		},
	}
	wantSpec := job.Spec
	wantSpec.Entrypoint.Command = append([]string(nil), job.Spec.Entrypoint.Command...)
	wantSpec.Entrypoint.Args = append([]string(nil), job.Spec.Entrypoint.Args...)

	manifest := managedManifest(t, job)
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	want := "raytrain-managed --nodes 2 --gpus-per-node 8 --cpus-per-node 32 --max-failures 7 --checkpoint-every-epochs 4 --checkpoint-keep-latest 9 --checkpoint-keep-best 2 -- python train.py --epochs 3"
	if entrypoint != want {
		t.Fatalf("managed checkpoint policy was not propagated exactly:\n got: %q\nwant: %q", entrypoint, want)
	}
	if !reflect.DeepEqual(job.Spec, wantSpec) {
		t.Fatalf("renderer mutated caller-owned spec:\n got: %#v\nwant: %#v", job.Spec, wantSpec)
	}
}

func TestManagedEntrypointPropagatesCheckpointPolicyBoundaries(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.Managed = domain.ManagedTrainingPolicy{
		MaxFailures: domain.ManagedMaxFailuresLimit,
		Checkpoint: domain.CheckpointPolicy{
			EveryEpochs: domain.ManagedCheckpointEveryEpochsLimit,
			KeepLatest:  domain.ManagedCheckpointRetentionLimit,
			KeepBest:    domain.ManagedCheckpointRetentionLimit,
		},
	}

	manifest := managedManifest(t, job)
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	want := "raytrain-managed --nodes 2 --gpus-per-node 8 --cpus-per-node 32 --max-failures 10 --checkpoint-every-epochs 100000 --checkpoint-keep-latest 1000 --checkpoint-keep-best 1000 -- python train.py --epochs 3"
	if entrypoint != want {
		t.Fatalf("managed checkpoint boundary policy was not propagated exactly:\n got: %q\nwant: %q", entrypoint, want)
	}
}

func TestLegacyEntrypointIgnoresManagedCheckpointPolicy(t *testing.T) {
	job := validRenderJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayDDP

	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render legacy RayJob: %v", err)
	}
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	want := "python train.py --epochs 3"
	if entrypoint != want {
		t.Fatalf("legacy entrypoint changed:\n got: %q\nwant: %q", entrypoint, want)
	}
	for _, flag := range []string{"--max-failures", "--checkpoint-every-epochs", "--checkpoint-keep-latest", "--checkpoint-keep-best"} {
		if strings.Contains(entrypoint, flag) {
			t.Fatalf("legacy entrypoint must not contain managed flag %q: %q", flag, entrypoint)
		}
	}
}

func TestManagedJobUsesEffectiveWorkerCPUInDriverArguments(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.Resources.CPUPerWorker = 0
	manifest := managedManifest(t, job)
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	if !strings.Contains(entrypoint, "--cpus-per-node 8") {
		t.Fatalf("managed driver CPU must match worker Pod fallback: %q", entrypoint)
	}
	cluster, _, _ := unstructured.NestedMap(manifest.Object, "spec", "rayClusterSpec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	containers, _, _ := nestedSlice(worker, "template", "spec", "containers")
	resources := containers[0].(map[string]any)["resources"].(map[string]any)
	for _, class := range []string{"requests", "limits"} {
		if got := resources[class].(map[string]any)["cpu"]; got != "8" {
			t.Fatalf("worker Pod CPU fallback changed in %s: %#v", class, got)
		}
	}
}

func TestManagedJobRuntimeEnvironmentReachesRayDriverAndWorkers(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	manifest := managedManifest(t, job)
	runtimeEnv, _, _ := unstructured.NestedString(manifest.Object, "spec", "runtimeEnvYAML")
	for _, expected := range []string{
		"RAY_TRAIN_V2_ENABLED: \"1\"",
		"PLATFORM_TRAINING_ENGINE: \"ray-train\"",
		"PLATFORM_JOB_ID: \"job-01\"",
	} {
		if !strings.Contains(runtimeEnv, expected) {
			t.Fatalf("managed runtimeEnvYAML missing %q: %q", expected, runtimeEnv)
		}
	}
}

func TestManagedJobPreservesRayClusterResourcesAndKeepsHeadCPUOnly(t *testing.T) {
	manifest := managedManifest(t, managedRenderJob(domain.RayVersionProduction))
	cluster, _, _ := unstructured.NestedMap(manifest.Object, "spec", "rayClusterSpec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	if len(workers) != 1 {
		t.Fatalf("managed job must retain one worker group, got %#v", workers)
	}
	worker := workers[0].(map[string]any)
	if worker["replicas"] != int64(2) || worker["minReplicas"] != int64(2) || worker["maxReplicas"] != int64(2) {
		t.Fatalf("managed worker replica contract changed: %#v", worker)
	}

	headContainers, _, _ := nestedSlice(cluster, "headGroupSpec", "template", "spec", "containers")
	headResources := headContainers[0].(map[string]any)["resources"].(map[string]any)
	for _, class := range []string{"requests", "limits"} {
		if _, hasGPU := headResources[class].(map[string]any)["nvidia.com/gpu"]; hasGPU {
			t.Fatalf("managed head must not request GPUs: %#v", headResources)
		}
	}
	workerContainers, _, _ := nestedSlice(worker, "template", "spec", "containers")
	workerResources := workerContainers[0].(map[string]any)["resources"].(map[string]any)
	for _, class := range []string{"requests", "limits"} {
		if got := workerResources[class].(map[string]any)["nvidia.com/gpu"]; got != "8" {
			t.Fatalf("managed workers must retain 8 GPUs in %s, got %#v", class, got)
		}
	}
}

func TestManagedMultiNodeAlwaysUsesTopologySpreadIndependentOfLegacyMode(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.Execution = domain.ExecutionProfile{Mode: domain.ExecutionModeLegacy}
	manifest := managedManifest(t, job)
	cluster, _, _ := unstructured.NestedMap(manifest.Object, "spec", "rayClusterSpec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	constraints, found, err := nestedSlice(worker, "template", "spec", "topologySpreadConstraints")
	if err != nil || !found || len(constraints) != 1 {
		t.Fatalf("managed multi-node job must enforce topology spread: found=%v err=%v constraints=%#v", found, err, constraints)
	}
	constraint := constraints[0].(map[string]any)
	if constraint["whenUnsatisfiable"] != "DoNotSchedule" || constraint["minDomains"] != int64(2) {
		t.Fatalf("unexpected managed topology spread: %#v", constraint)
	}
}
