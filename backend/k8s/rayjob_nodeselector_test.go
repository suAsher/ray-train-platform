package k8s

import (
	"testing"
)

func podSpecFor(t *testing.T, options RenderOptions, group string) map[string]any {
	t.Helper()
	manifest, err := RenderRayJob(validRenderJob(), options)
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	path := []string{"spec", "rayClusterSpec", "headGroupSpec", "template", "spec"}
	if group == "worker" {
		groups, _, err := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
		if err != nil || len(groups) == 0 {
			t.Fatalf("read worker groups: %v", err)
		}
		worker, _ := groups[0].(map[string]any)
		template, _ := worker["template"].(map[string]any)
		spec, _ := template["spec"].(map[string]any)
		return spec
	}
	spec, found, err := nestedMap(manifest.Object, path...)
	if err != nil || !found {
		t.Fatalf("read head pod spec: %v", err)
	}
	return spec
}

func selectorOf(spec map[string]any) map[string]any {
	selector, _ := spec["nodeSelector"].(map[string]any)
	return selector
}

// Adding GPU machines must not require a code change: the label that pins Ray
// Pods to the training pool comes from configuration.
func TestNodeSelectorIsConfigurable(t *testing.T) {
	options := testRenderOptions()
	options.NodeSelector = map[string]string{"accelerator": "nvidia-h100", "pool": "training"}

	worker := selectorOf(podSpecFor(t, options, "worker"))
	if worker["accelerator"] != "nvidia-h100" || worker["pool"] != "training" {
		t.Fatalf("worker must use the configured selector, got %v", worker)
	}
}

func TestNodeSelectorDefaultsToTrainingPoolLabel(t *testing.T) {
	worker := selectorOf(podSpecFor(t, testRenderOptions(), "worker"))
	if worker["accelerator"] != "nvidia-rtx-4090" {
		t.Fatalf("expected the default training label, got %v", worker)
	}
}

// The head holds the GCS and the job submission server. If it lands on a
// serverless virtual node it cannot reach the workers reliably, so it is
// pinned to the same real node pool as the workers.
func TestHeadPodIsPinnedToTheTrainingPool(t *testing.T) {
	options := testRenderOptions()
	options.NodeSelector = map[string]string{"accelerator": "nvidia-h100"}

	head := selectorOf(podSpecFor(t, options, "head"))
	if head["accelerator"] != "nvidia-h100" {
		t.Fatalf("head must be pinned to the training pool, got %v", head)
	}
}
