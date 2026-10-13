package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/domain"
)

func capacityTestNode(name string) *corev1.Node {
	return trainingNode(name, map[string]string{"accelerator": "nvidia-rtx-4090", "platform.wellspiking.ai/cache-ready": "true", "kubernetes.io/hostname": name}, "8", "64", "256Gi")
}

func capacityTestClient(objects []runtime.Object, workloads ...runtime.Object) *Client {
	gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	return NewClientFromInterfaces(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"}, workloads...), fake.NewSimpleClientset(objects...))
}

func capacityTestRequest() SchedulingCapacityRequest {
	return SchedulingCapacityRequest{TenantID: "local", AcceleratorClass: domain.AcceleratorRTX4090, Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 8, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}}
}

func TestSchedulingCapacityCountsDebugAndKeepsFragmentedShapeDistinct(t *testing.T) {
	p1 := teamWorkerPod("debug", "local", "", "a", domain.AcceleratorRTX4090, corev1.PodRunning, 1)
	p1.Labels = map[string]string{"ray.io/cluster": "dev-test", "ray.io/node-type": "worker"}
	p2 := teamWorkerPod("small", "other", "job-small", "b", domain.AcceleratorRTX4090, corev1.PodRunning, 3)
	c := capacityTestClient([]runtime.Object{capacityTestNode("a"), capacityTestNode("b"), p1, p2})
	s, err := c.SchedulingCapacity(context.Background(), capacityTestRequest())
	if err != nil || !s.Available || s.Pool.TotalGPUs != 16 || s.Pool.FreeGPUs != 12 || s.Pool.AllocatedGPUs != 4 || s.Fit.CanFit || s.Fit.WorkerSlots != 0 || s.Fit.Reason != "gpu_fragmentation" {
		t.Fatalf("capacity=%+v err=%v", s, err)
	}
}

func TestSchedulingCapacityDeduplicatesBoundPodAndSlicesReservation(t *testing.T) {
	p := teamWorkerPod("worker", "local", "job-a", "a", domain.AcceleratorRTX4090, corev1.PodRunning, 2)
	p.Labels["kueue.x-k8s.io/workload-name"] = "wl-a"
	p.Labels["kueue.x-k8s.io/podset"] = "workers"
	w := capacityTestWorkload("wl-a", "a", "workers", 2, "4", true)
	c := capacityTestClient([]runtime.Object{capacityTestNode("a"), p}, w)
	r := capacityTestRequest()
	r.Resources.GPUsPerWorker = 1
	s, err := c.SchedulingCapacity(context.Background(), r)
	if err != nil || !s.Available || s.Pool.AllocatedGPUs != 2 || s.Pool.ReservedGPUs != 2 || s.Pool.FreeGPUs != 4 {
		t.Fatalf("bound and reservation must count once: capacity=%+v err=%v", s, err)
	}
}

func TestSchedulingCapacitySupportsLegacyDomainsReservation(t *testing.T) {
	c := capacityTestClient([]runtime.Object{capacityTestNode("a")}, capacityTestWorkload("wl-a", "a", "workers", 1, "8", false))
	s, err := c.SchedulingCapacity(context.Background(), capacityTestRequest())
	if err != nil || !s.Available || s.Pool.ReservedGPUs != 8 || s.Pool.FreeGPUs != 0 {
		t.Fatalf("capacity=%+v err=%v", s, err)
	}
}

func capacityTestWorkload(name, node, podset string, count int64, gpu string, slices bool) *unstructured.Unstructured {
	topology := map[string]any{"levels": []any{"kubernetes.io/hostname"}, "domains": []any{map[string]any{"count": count, "values": []any{node}}}}
	if slices {
		delete(topology, "domains")
		topology["slices"] = []any{map[string]any{"domainCount": int64(1), "podCounts": map[string]any{"universal": count}, "valuesPerLevel": []any{map[string]any{"universal": node}}}}
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload", "metadata": map[string]any{"name": name, "namespace": "tenant-local"}, "status": map[string]any{"admission": map[string]any{"podSetAssignments": []any{map[string]any{"name": podset, "count": count, "resourceUsage": map[string]any{"nvidia.com/gpu": gpu, "cpu": "8", "memory": "32Gi"}, "topologyAssignment": topology}}}}}}
}

func TestSchedulingPodRequestsRestartableInitAndOverhead(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	p := corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}}},
		InitContainers: []corev1.Container{
			{RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}},
			{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}}},
		}, Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
	}}
	requests := SchedulingPodRequests(p)
	if got := requests[corev1.ResourceCPU]; got.MilliValue() != 9500 {
		t.Fatalf("requests=%s, want 9500m", got.String())
	}
}

func TestSchedulingCapacityRejectsUnknownReservationAndFiltersConstraints(t *testing.T) {
	n := capacityTestNode("a")
	n.Spec.Taints = []corev1.Taint{{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule}}
	c := capacityTestClient([]runtime.Object{n})
	s, err := c.SchedulingCapacity(context.Background(), capacityTestRequest())
	if err != nil || !s.Available || s.Pool.TotalGPUs != 0 || s.Fit.CanFit {
		t.Fatalf("tainted node must be excluded: %+v %v", s, err)
	}
	w := capacityTestWorkload("wl-a", "a", "workers", 1, "8", true)
	assignments, _, _ := unstructured.NestedSlice(w.Object, "status", "admission", "podSetAssignments")
	delete(assignments[0].(map[string]any), "topologyAssignment")
	_ = unstructured.SetNestedSlice(w.Object, assignments, "status", "admission", "podSetAssignments")
	c = capacityTestClient([]runtime.Object{capacityTestNode("a")}, w)
	s, err = c.SchedulingCapacity(context.Background(), capacityTestRequest())
	if err == nil || s.Available {
		t.Fatalf("unmapped reservation must be unknown: %+v %v", s, err)
	}
}
