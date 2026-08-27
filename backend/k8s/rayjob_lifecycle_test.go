package k8s

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func renderSpec(t *testing.T, job domain.TrainingJob) map[string]any {
	t.Helper()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, found, err := unstructured.NestedMap(manifest.Object, "spec")
	if err != nil || !found {
		t.Fatalf("manifest has no spec: %v", err)
	}
	return spec
}

func TestPreUpgradeRayTrainJobKeepsLegacyLifecycleAndLauncherFields(t *testing.T) {
	job := validRenderJob()
	job.Spec.TrainingEngine = ""
	job.Spec.RayVersion = ""
	job.Spec.Execution = domain.ExecutionProfile{Mode: domain.ExecutionModeRayTrain}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render pre-upgrade job: %v", err)
	}

	spec, _, _ := unstructured.NestedMap(manifest.Object, "spec")
	if got := spec["entrypoint"]; got != "raytrain-launch --mode ray_train --workers 2 --gpus-per-worker 8 -- python train.py --epochs 3" {
		t.Fatalf("legacy launcher changed: %#v", got)
	}
	cluster := spec["rayClusterSpec"].(map[string]any)
	if got := cluster["rayVersion"]; got != domain.RayVersionLegacy {
		t.Fatalf("legacy Ray version changed: %#v", got)
	}
	workers := cluster["workerGroupSpecs"].([]any)
	worker := workers[0].(map[string]any)
	constraints, found, _ := nestedSlice(worker, "template", "spec", "topologySpreadConstraints")
	if !found || len(constraints) != 1 {
		t.Fatalf("legacy multi-node topology spread changed: %#v", constraints)
	}
	if spec["suspend"] != true || spec["ttlSecondsAfterFinished"] != int64(defaultFailureCleanupTTLSeconds) {
		t.Fatalf("legacy lifecycle changed: suspend=%#v ttl=%#v", spec["suspend"], spec["ttlSecondsAfterFinished"])
	}
	runtimeEnv := spec["runtimeEnvYAML"].(string)
	encoded := fmt.Sprintf("%#v", manifest.Object)
	for _, forbidden := range []string{"RAY_TRAIN_V2_ENABLED", "PLATFORM_TRAINING_ENGINE", "PLATFORM_JOB_ID", "callback", "Callback"} {
		if strings.Contains(runtimeEnv, forbidden) || strings.Contains(encoded, forbidden) {
			t.Fatalf("pre-upgrade job gained managed field %q", forbidden)
		}
	}
}

// A finished RayJob must tear its RayCluster down, otherwise every completed
// training run keeps holding its GPUs until an operator deletes it by hand.
func TestRenderRayJobShutsDownClusterAfterJobFinishes(t *testing.T) {
	spec := renderSpec(t, validRenderJob())
	if shutdown, _ := spec["shutdownAfterJobFinishes"].(bool); !shutdown {
		t.Fatalf("expected shutdownAfterJobFinishes=true, got %v", spec["shutdownAfterJobFinishes"])
	}
}

func TestRenderRayJobAppliesCleanupTTLFromPolicy(t *testing.T) {
	job := validRenderJob()
	job.Spec.CleanupPolicy = domain.CleanupPolicy{FailureTTLSeconds: 900}
	spec := renderSpec(t, job)
	ttl, ok := spec["ttlSecondsAfterFinished"].(int64)
	if !ok || ttl != 900 {
		t.Fatalf("expected initial failure-safe ttlSecondsAfterFinished=900, got %v", spec["ttlSecondsAfterFinished"])
	}
}

func TestRenderRayJobAppliesDefaultCleanupTTLWhenPolicyOmitted(t *testing.T) {
	spec := renderSpec(t, validRenderJob())
	ttl, ok := spec["ttlSecondsAfterFinished"].(int64)
	if !ok || ttl != 600 {
		t.Fatalf("expected unfinished/failed training to retain diagnostics for 600 seconds, got %v", spec["ttlSecondsAfterFinished"])
	}
}

// Kueue admits a RayJob by flipping suspend to false. Creating it already
// running lets the job bypass the queue and the tenant GPU quota.
func TestRenderRayJobIsSuspendedForKueueAdmission(t *testing.T) {
	spec := renderSpec(t, validRenderJob())
	if suspend, _ := spec["suspend"].(bool); !suspend {
		t.Fatalf("expected suspend=true when a queue is set, got %v", spec["suspend"])
	}
}

func TestRenderRayJobAppliesTimeoutAsActiveDeadline(t *testing.T) {
	job := validRenderJob()
	job.Spec.TimeoutSeconds = 3600
	spec := renderSpec(t, job)
	deadline, ok := spec["activeDeadlineSeconds"].(int64)
	if !ok || deadline != 3600 {
		t.Fatalf("expected activeDeadlineSeconds=3600, got %v", spec["activeDeadlineSeconds"])
	}
}

func TestRenderRayJobPassesConfiguredInfrastructureRetriesToSubmitter(t *testing.T) {
	job := validRenderJob()
	job.Spec.RetryPolicy = domain.RetryPolicy{MaxRetries: 2}
	spec := renderSpec(t, job)
	config, ok := spec["submitterConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected submitterConfig, got %#v", spec["submitterConfig"])
	}
	if retries, ok := config["backoffLimit"].(int64); !ok || retries != 2 {
		t.Fatalf("expected submitter backoffLimit=2, got %#v", config["backoffLimit"])
	}
}

func TestRenderRayJobOmitsActiveDeadlineWhenNoTimeout(t *testing.T) {
	spec := renderSpec(t, validRenderJob())
	if _, present := spec["activeDeadlineSeconds"]; present {
		t.Fatalf("no timeout configured, activeDeadlineSeconds must be omitted")
	}
}
