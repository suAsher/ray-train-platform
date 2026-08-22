package k8s

import "testing"

var testMLflowProvenanceKey = []byte("0123456789abcdef0123456789abcdef")

func TestRenderRayJobInjectsSecretlessMLflowContractIntoHeadAndWorkers(t *testing.T) {
	job := validRenderJob()
	options := testRenderOptions()
	options.MLflow = MLflowOptions{Enabled: true, TrackingURI: "http://mlflow-ingest.mlflow-system.svc.cluster.local:8080", ExperimentPrefix: "raytrain", ProvenanceKey: testMLflowProvenanceKey}

	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render RayJob: %v", err)
	}
	head, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	workers, _, _ := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	workerSpec, _, _ := nestedMap(worker, "template", "spec")

	want := map[string]string{
		"MLFLOW_TRACKING_URI":        options.MLflow.TrackingURI,
		"MLFLOW_EXPERIMENT_NAME":     "raytrain-tenant-a",
		"MLFLOW_RUN_NAME":            job.ID,
		"RAYTRAIN_JOB_ID":            job.ID,
		"RAYTRAIN_TENANT_ID":         job.TenantID,
		"RAYTRAIN_SUBMITTER_USER_ID": job.UserID,
		"RAYTRAIN_MLFLOW_PROVENANCE": mlflowProvenanceTag(testMLflowProvenanceKey, job.ID),
	}
	for name, spec := range map[string]map[string]any{"head": head, "worker": workerSpec} {
		env := podEnvironment(spec)
		for key, expected := range want {
			if env[key] != expected {
				t.Fatalf("%s environment %s=%q, want %q", name, key, env[key], expected)
			}
		}
		for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "TOS_ACCESS_KEY", "TOS_SECRET_KEY", "MLFLOW_TRACKING_TOKEN"} {
			if _, exists := env[forbidden]; exists {
				t.Fatalf("%s must not receive credential %s", name, forbidden)
			}
		}
	}
}

func TestRenderRayJobRejectsMLflowWithoutProvenanceKey(t *testing.T) {
	options := testRenderOptions()
	options.MLflow = MLflowOptions{Enabled: true, TrackingURI: "http://mlflow-ingest.mlflow-system.svc.cluster.local:8080", ExperimentPrefix: "raytrain"}
	if _, err := RenderRayJob(validRenderJob(), options); err == nil {
		t.Fatal("MLflow integration must fail closed without an unforgeable provenance key")
	}
}

func TestRenderRayJobOmitsMLflowContractWhenDisabled(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if _, exists := podEnvironment(head)["MLFLOW_TRACKING_URI"]; exists {
		t.Fatal("disabled MLflow must not change existing Ray workloads")
	}
}
