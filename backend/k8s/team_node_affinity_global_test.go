package k8s

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/domain"
)

func compactPlacementNode(name string) *corev1.Node {
	return trainingNode(name, map[string]string{"accelerator": "nvidia-rtx-4090", "kubernetes.io/hostname": name, "raytrain.io/cache-ready": "true"}, "8", "64", "512Gi")
}

func TestPreferredTeamWorkerNodesCompactsGlobalBeforeTeam(t *testing.T) {
	client := &Client{dynamic: compactPlacementDynamic(), kubernetes: fake.NewSimpleClientset(
		compactPlacementNode("node-other"), compactPlacementNode("node-team"), compactPlacementNode("node-empty"),
		teamWorkerPod("other", "tenant-b", "job-other", "node-other", domain.AcceleratorRTX4090, corev1.PodRunning, 6),
		teamWorkerPod("team", "tenant-a", "job-team", "node-team", domain.AcceleratorRTX4090, corev1.PodRunning, 1),
	)}
	nodes, err := client.PreferredTeamWorkerNodes(context.Background(), TeamWorkerNodePreferenceRequest{TenantID: "tenant-a", JobID: "job-new", AcceleratorClass: domain.AcceleratorRTX4090})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-other", "node-team", "node-empty"}; !reflect.DeepEqual(nodes, want) {
		t.Fatalf("global compact must precede same-team affinity: got %v want %v", nodes, want)
	}
}

func TestPreferredTeamWorkerNodesCountsDebugAndTerminatingPods(t *testing.T) {
	debug := teamWorkerPod("debug", "tenant-b", "", "node-debug", domain.AcceleratorRTX4090, corev1.PodRunning, 5)
	debug.Labels = map[string]string{"ray.io/cluster": "dev-example", "ray.io/group": "dev-workers", "ray.io/node-type": "worker"}
	terminating := teamWorkerPod("terminating", "tenant-b", "job-old", "node-terminating", domain.AcceleratorRTX4090, corev1.PodRunning, 7)
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	client := &Client{dynamic: compactPlacementDynamic(), kubernetes: fake.NewSimpleClientset(
		compactPlacementNode("node-debug"), compactPlacementNode("node-terminating"), compactPlacementNode("node-finished"),
		debug, terminating,
		teamWorkerPod("finished", "tenant-a", "job-finished", "node-finished", domain.AcceleratorRTX4090, corev1.PodSucceeded, 8),
	)}
	nodes, err := client.PreferredTeamWorkerNodes(context.Background(), TeamWorkerNodePreferenceRequest{TenantID: "tenant-a", JobID: "job-new", AcceleratorClass: domain.AcceleratorRTX4090})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-terminating", "node-debug", "node-finished"}; !reflect.DeepEqual(nodes, want) {
		t.Fatalf("debug and terminating requests still occupy GPU capacity: got %v want %v", nodes, want)
	}
}

func TestRenderOptionsCompactPreferenceUsesWorkerShape(t *testing.T) {
	cpuFull := teamWorkerPod("cpu-full", "tenant-b", "job-other", "node-full", domain.AcceleratorRTX4090, corev1.PodRunning, 6)
	cpuFull.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("64")
	client := &Client{dynamic: compactPlacementDynamic(), kubernetes: fake.NewSimpleClientset(
		compactPlacementNode("node-full"), compactPlacementNode("node-fit"), compactPlacementNode("node-empty"), cpuFull,
		teamWorkerPod("fit", "tenant-b", "job-fit", "node-fit", domain.AcceleratorRTX4090, corev1.PodRunning, 5),
	)}
	reconciler := NewReconciler(nil, client, RenderOptions{})
	job := validRenderJob()
	job.TenantID = "tenant-a"
	job.Spec.Resources = domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 2, CPUPerWorker: 8, MemoryPerWorker: "32Gi"}
	options := reconciler.withTeamNodeAffinity(context.Background(), job, RenderOptions{TopologyAwareScheduling: true, TeamNodeAffinityEnabled: true})
	if want := []string{"node-fit", "node-empty"}; !reflect.DeepEqual(options.TeamNodeAffinityPreference, want) {
		t.Fatalf("CPU-exhausted nodes must not receive compact placement preference: got %v want %v", options.TeamNodeAffinityPreference, want)
	}
}

func TestPreferredTeamWorkerNodesUsesTeamOnlyAsTieBreak(t *testing.T) {
	client := &Client{dynamic: compactPlacementDynamic(), kubernetes: fake.NewSimpleClientset(
		compactPlacementNode("node-a-other"), compactPlacementNode("node-z-team"),
		teamWorkerPod("other", "tenant-b", "job-other", "node-a-other", domain.AcceleratorRTX4090, corev1.PodRunning, 4),
		teamWorkerPod("team", "tenant-a", "job-team", "node-z-team", domain.AcceleratorRTX4090, corev1.PodRunning, 4),
	)}
	nodes, err := client.PreferredTeamWorkerNodes(context.Background(), TeamWorkerNodePreferenceRequest{TenantID: "tenant-a", JobID: "job-new", AcceleratorClass: domain.AcceleratorRTX4090})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-z-team", "node-a-other"}; !reflect.DeepEqual(nodes, want) {
		t.Fatalf("same-team affinity applies only after global compact scores tie: got %v want %v", nodes, want)
	}
}

func compactPlacementDynamic() dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}: "WorkloadList",
		{Group: "kueue.x-k8s.io", Version: "v1beta1", Resource: "workloads"}: "WorkloadList",
	})
}

func TestPreferredTeamWorkerNodesHonorsShapeAndHardConstraints(t *testing.T) {
	good := compactPlacementNode("node-fit")
	good.Labels["pool"] = "production"
	excluded := compactPlacementNode("node-other-pool")
	excluded.Labels["pool"] = "staging"
	gpuFull := teamWorkerPod("gpu-full", "tenant-b", "job-gpu", "node-gpu-full", domain.AcceleratorRTX4090, corev1.PodRunning, 7)
	memoryFull := teamWorkerPod("memory-full", "tenant-b", "job-memory", "node-memory-full", domain.AcceleratorRTX4090, corev1.PodRunning, 6)
	memoryFull.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("500Gi")
	gpuNode, memoryNode := compactPlacementNode("node-gpu-full"), compactPlacementNode("node-memory-full")
	gpuNode.Labels["pool"], memoryNode.Labels["pool"] = "production", "production"
	client := &Client{dynamic: compactPlacementDynamic(), kubernetes: fake.NewSimpleClientset(
		good, excluded, gpuNode, memoryNode, gpuFull, memoryFull,
		teamWorkerPod("other-pool", "tenant-a", "job-other-pool", "node-other-pool", domain.AcceleratorRTX4090, corev1.PodRunning, 7),
	)}
	request := TeamWorkerNodePreferenceRequest{
		TenantID: "tenant-a", AcceleratorClass: domain.AcceleratorRTX4090,
		NodeSelector: map[string]string{"accelerator": "nvidia-rtx-4090", "pool": "production"},
		Resources:    domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 2, CPUPerWorker: 8, MemoryPerWorker: "32Gi"},
	}
	nodes, err := client.PreferredTeamWorkerNodes(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-fit"}; !reflect.DeepEqual(nodes, want) {
		t.Fatalf("shape/selector filtering: got %v want %v", nodes, want)
	}
	request.DedicatedNodes = map[string][]string{"tenant-b": {"node-fit"}}
	nodes, err = client.PreferredTeamWorkerNodes(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("other tenant dedicated node must not be preferred: %v", nodes)
	}
}

func TestPreferredTeamWorkerNodesRefusesIncompleteCapacity(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(compactPlacementNode("node-a"))}
	nodes, err := client.PreferredTeamWorkerNodes(context.Background(), TeamWorkerNodePreferenceRequest{TenantID: "tenant-a", AcceleratorClass: domain.AcceleratorRTX4090})
	if err == nil || len(nodes) != 0 {
		t.Fatalf("unreadable Kueue reservations must not produce placement hints: nodes=%v err=%v", nodes, err)
	}
}
