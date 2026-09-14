package k8s

import (
	"fmt"
	"testing"

	"ray-train-platform-backend/domain"
)

// TAS accounts for other workloads before assigning hosts. Do not add a
// Kubernetes spread rule which can reject the hosts already reserved by TAS.
func TestRayJobTASPackingDoesNotConflictWithPodPlacement(t *testing.T) {
	for _, engine := range []domain.TrainingEngine{domain.TrainingEngineRayDDP, domain.TrainingEngineRayTrain} {
		for _, replicas := range []int{1, 2} {
			for _, gpus := range []int{1, 2, 4, 8} {
				t.Run(fmt.Sprintf("%s/%dx%d", engine, replicas, gpus), func(t *testing.T) {
					job := validRenderJob()
					job.Spec.TrainingEngine = engine
					job.Spec.Execution = domain.ExecutionProfile{Mode: domain.ExecutionModeRayTrain}
					if replicas == 1 {
						job.Spec.Execution.Mode = domain.ExecutionModeTorchrun
						if gpus == 1 {
							job.Spec.Execution.Mode = domain.ExecutionModeSingleGPU
						}
					}
					job.Spec.Resources.WorkerReplicas = replicas
					job.Spec.Resources.GPUsPerWorker = gpus
					if engine == domain.TrainingEngineRayTrain {
						job.Spec.RayVersion = domain.RayVersionProduction
					}
					options := testRenderOptions()
					options.TopologyAwareScheduling = true
					options.managedCreationFence = 1
					manifest, err := RenderRayJob(job, options)
					if err != nil {
						t.Fatal(err)
					}
					workers, _, _ := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
					worker := workers[0].(map[string]any)
					annotations, _, _ := nestedMap(worker, "template", "metadata", "annotations")
					if annotations["kueue.x-k8s.io/podset-unconstrained-topology"] != "true" {
						t.Errorf("worker must select TAS least-free-capacity packing: %#v", annotations)
					}
					for _, key := range []string{"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-required-topology"} {
						if _, found := annotations[key]; found {
							t.Errorf("conflicting topology intent %s", key)
						}
					}
					pod, _, _ := nestedMap(worker, "template", "spec")
					if _, found := pod["topologySpreadConstraints"]; found {
						t.Errorf("spread conflicts with TAS assignment: %#v", pod["topologySpreadConstraints"])
					}
					if worker["replicas"] != int64(replicas) {
						t.Fatal("packing changed worker count")
					}
					containers, _, _ := nestedSlice(pod, "containers")
					resources := containers[0].(map[string]any)["resources"].(map[string]any)
					if resources["requests"].(map[string]any)["nvidia.com/gpu"] != fmt.Sprint(gpus) {
						t.Fatal("packing changed GPU request")
					}
				})
			}
		}
	}
}
