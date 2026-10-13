package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSchedulingCapacityOtherPoolUnmappedReservationDoesNotHideCurrentPool(t *testing.T) {
	w := capacityTestWorkload("a100-workload", "unrelated-a100-node", "workers", int64(1), "8", true)
	assignments, _, _ := unstructured.NestedSlice(w.Object, "status", "admission", "podSetAssignments")
	a := assignments[0].(map[string]any)
	delete(a, "topologyAssignment")
	a["flavors"] = map[string]any{"nvidia.com/gpu": "gpu-a100-flavor", "cpu": "gpu-a100-flavor", "memory": "gpu-a100-flavor"}
	_ = unstructured.SetNestedSlice(w.Object, assignments, "status", "admission", "podSetAssignments")
	flavor := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "ResourceFlavor", "metadata": map[string]any{"name": "gpu-a100-flavor"}, "spec": map[string]any{"nodeLabels": map[string]any{"accelerator": "nvidia-a100"}}}}
	c := capacityTestClient([]runtime.Object{capacityTestNode("a")}, w, flavor)
	s, err := c.SchedulingCapacity(context.Background(), capacityTestRequest())
	if err != nil || !s.Available || s.Pool.FreeGPUs != 8 || !s.Fit.CanFit {
		t.Fatalf("other GPU pool must be isolated: capacity=%+v err=%v", s, err)
	}
}
