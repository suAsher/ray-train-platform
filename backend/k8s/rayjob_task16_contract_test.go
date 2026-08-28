package k8s

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTask16HarnessOwnershipSelectorMatchesRenderedPodLabels(t *testing.T) {
	job := validRenderJob()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render RayJob: %v", err)
	}
	cluster, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	labels, ok, err := nestedMap(worker, "template", "metadata", "labels")
	if err != nil || !ok {
		t.Fatalf("worker template labels missing: %v", err)
	}
	if labels["platform_job_id"] != job.ID || labels["platform_tenant_id"] != job.TenantID {
		t.Fatalf("worker identity labels do not match persisted identity: %#v", labels)
	}

	_, current, _, _ := runtime.Caller(0)
	scriptPath := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "scripts", "e2e-training.sh"))
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read Task 16 harness: %v", err)
	}
	text := string(script)
	if !strings.Contains(text, "ACCEPTANCE_LABEL_KEY='platform_job_id'") || !strings.Contains(text, "ACCEPTANCE_TENANT_LABEL_KEY='platform_tenant_id'") {
		t.Fatal("Task 16 selectors do not use the backend-rendered immutable pod labels")
	}
}
