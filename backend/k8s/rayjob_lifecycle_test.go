package k8s

import (
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
