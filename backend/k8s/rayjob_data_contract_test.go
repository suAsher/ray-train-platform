package k8s

import "testing"

func TestRenderRayJobExposesPortableDataContract(t *testing.T) {
	job := validRenderJob()
	job.Spec.DatasetURI = "tos://training-data/datasets/support-v1/"
	job.Spec.CheckpointURI = "tos://training-data/checkpoints/base-model/"
	job.Spec.OutputURI = "tos://training-data/outputs/support-v1-run-01/"

	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}

	headSpec, found, err := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if err != nil || !found {
		t.Fatalf("read head pod spec: %v", err)
	}
	workers, found, err := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("read worker group: %v", err)
	}
	worker, ok := workers[0].(map[string]any)
	if !ok {
		t.Fatalf("read worker group: %#v", workers[0])
	}
	workerSpec, found, err := nestedMap(worker, "template", "spec")
	if err != nil || !found {
		t.Fatalf("read worker pod spec: %v", err)
	}

	want := map[string]string{
		"PLATFORM_DATASET_URI":    job.Spec.DatasetURI,
		"PLATFORM_CHECKPOINT_URI": job.Spec.CheckpointURI,
		"PLATFORM_OUTPUT_URI":     job.Spec.OutputURI,
	}
	for name, spec := range map[string]map[string]any{"head": headSpec, "worker": workerSpec} {
		got := podEnvironment(spec)
		for key, expected := range want {
			if got[key] != expected {
				t.Fatalf("%s pod must expose %s=%q, got %#v", name, key, expected, got)
			}
		}
	}
}

func podEnvironment(podSpec map[string]any) map[string]string {
	values := map[string]string{}
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		return values
	}
	container, _ := containers[0].(map[string]any)
	env, _ := container["env"].([]any)
	for _, item := range env {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		value, _ := entry["value"].(string)
		if name != "" {
			values[name] = value
		}
	}
	return values
}
