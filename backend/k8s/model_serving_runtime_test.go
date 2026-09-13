package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func servingRenderFixture() domain.TrainingJob {
	job := evaluationArchiveJob()
	job.SubmissionOrigin = domain.SubmissionOriginServing
	job.Spec.EvaluationRuntime = nil
	job.Spec.Source.Type = "serving-archive"
	job.Spec.Resources = domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}
	job.Spec.DataMode = domain.DataModeMount
	job.Spec.Entrypoint = domain.Entrypoint{Command: []string{"python", "smoke_adapter.py"}}
	job.Spec.ServingRuntime = &domain.ServingRuntime{DeploymentID: "deployment-1", ModelSHA256: strings.Repeat("a", 64), ModelSizeBytes: 156, CodeID: job.Spec.Source.ArtifactID, CodeSHA256: job.Spec.Source.ArtifactSHA256, CodeSizeBytes: 1024, CodeFormat: "zip", Protocol: "model-serving-http/v1"}
	return job
}
func TestServingRuntimeJSONCannotSupplyTrustedContext(t *testing.T) {
	var spec domain.JobSpec
	if err := json.Unmarshal([]byte(`{"servingRuntime":{"DeploymentID":"forged"}}`), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ServingRuntime != nil {
		t.Fatal("public JSON supplied trusted serving context")
	}
	spec.ServingRuntime = servingRenderFixture().Spec.ServingRuntime
	raw, _ := json.Marshal(spec)
	if strings.Contains(string(raw), "deployment-1") {
		t.Fatal("private serving context serialized")
	}
}
func TestServingCodeAndEnvironmentUseOnlyInternalJobContext(t *testing.T) {
	job := servingRenderFixture()
	options := testRenderOptions()
	options.TrainingEventBaseURL = "http://backend.platform.svc:8080/api/v1/internal"
	options.trainingEventJobID = job.ID
	command := servingCodeMaterializerCommand(job.Spec, options)
	for _, expected := range []string{"platform-fetch-serving-code.py", "platform-safe-extract.py", job.ID, "1024", job.Spec.ServingRuntime.CodeSHA256} {
		if !strings.Contains(command, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	env, _ := json.Marshal(appendServingRuntime(nil, job.Spec.ServingRuntime, options))
	for _, expected := range []string{"MODEL_SERVING_PORT", "8000", "MODEL_SERVING_MODEL_SIZE_BYTES", "/jobs/" + job.ID + "/model-serving"} {
		if !strings.Contains(string(env), expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	options.TrainingEventBaseURL = "https://external.example/api/v1/internal"
	if !strings.Contains(servingCodeMaterializerCommand(job.Spec, options), "exit 1") {
		t.Fatal("external serving code endpoint accepted")
	}
}
func TestServingRuntimeRestorePreservesAttemptAndRejectsIdentityMismatch(t *testing.T) {
	loaded := servingRenderFixture()
	current := loaded
	current.Spec.ServingRuntime = nil
	current.ClusterAttempt = 2
	current.RayJobUID = "current-uid"
	store := &runtimeReloadStore{memoryJobStore: &memoryJobStore{job: &loaded}, loaded: &loaded}
	reconciler := NewReconciler(store, nil, testRenderOptions())
	restored, err := reconciler.restoreServingRuntime(context.Background(), &current)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Spec.ServingRuntime == nil || restored.ClusterAttempt != 2 || restored.RayJobUID != "current-uid" || current.Spec.ServingRuntime != nil {
		t.Fatal("restore lost attempt or mutated input")
	}
	loaded.UserID = "foreign"
	if _, err := reconciler.restoreServingRuntime(context.Background(), &current); err == nil {
		t.Fatal("foreign runtime copied")
	}
	loaded = servingRenderFixture()
	loaded.Spec.Source.ArtifactID = "different"
	loaded.Spec.ServingRuntime.CodeID = strings.Repeat("f", 32)
	if _, err := reconciler.restoreServingRuntime(context.Background(), &current); err == nil {
		t.Fatal("mismatched code copied")
	}
}
