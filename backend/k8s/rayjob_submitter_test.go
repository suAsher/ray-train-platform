package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// KubeRay runs `ray job submit` in a submitter Pod and Ray uploads the
// runtime env's working_dir from *that* Pod. Without the source materialized
// there, the driver receives an empty working directory — or the submitter
// dies with "directory /workspace must be an existing directory". Only inline
// `python -c` entrypoints survive that, which is why it went unnoticed.
func TestRenderRayJobMaterializesSourceInSubmitterPod(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, found, err := unstructured.NestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if err != nil || !found {
		t.Fatalf("RayJob must define a submitter pod template so the working dir exists: %v", err)
	}

	initContainers, _ := spec["initContainers"].([]any)
	if len(initContainers) == 0 {
		t.Fatalf("submitter pod needs the source materializer init container")
	}
	container, _ := initContainers[0].(map[string]any)
	if name, _ := container["name"].(string); name != "source-materializer" {
		t.Fatalf("unexpected init container %q", name)
	}

	containers, _ := spec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatalf("submitter pod needs a container")
	}
	main, _ := containers[0].(map[string]any)
	mounts, _ := main["volumeMounts"].([]any)
	mounted := false
	for _, item := range mounts {
		mount, _ := item.(map[string]any)
		if path, _ := mount["mountPath"].(string); path == "/workspace" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("submitter must mount /workspace, got %v", mounts)
	}
}

// A batch Job's pod spec requires restartPolicy. KubeRay uses this template
// verbatim, so omitting it makes the submitter Job invalid and the RayJob
// stalls in Initializing with FailedToCreateRayJobSubmitter.
func TestSubmitterPodSetsRestartPolicy(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if policy, _ := spec["restartPolicy"].(string); policy != "Never" {
		t.Fatalf("submitter pod must set restartPolicy=Never, got %q", policy)
	}
}

// The submitter only uploads code and waits; giving it a GPU would hold a card
// for the whole run.
func TestSubmitterPodRequestsNoGPU(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	containers, _ := spec["containers"].([]any)
	main, _ := containers[0].(map[string]any)
	resources, _ := main["resources"].(map[string]any)
	limits, _ := resources["limits"].(map[string]any)
	if _, present := limits["nvidia.com/gpu"]; present {
		t.Fatalf("submitter pod must not request a GPU: %v", limits)
	}
}
