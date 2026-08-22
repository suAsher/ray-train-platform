package k8s

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestSyncClusterQueueQuotaUsesKueueV1beta2(t *testing.T) {
	installedGVR := schema.GroupVersionResource{
		Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "clusterqueues",
	}
	existing := clusterQueueObject("cluster-gpu-queue", "1", "16", "64Gi")
	existing.SetAPIVersion(installedGVR.Group + "/" + installedGVR.Version)

	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{installedGVR: "ClusterQueueList"},
		existing,
	)
	client := &Client{dynamic: dynamic}
	capacity := TrainingPoolCapacity{Nodes: 3, GPUs: 24, CPUMillis: 192_000, MemoryBytes: 3 * 512 * 1024 * 1024 * 1024}

	changed, err := client.SyncClusterQueueQuota(context.Background(), "cluster-gpu-queue", capacity)
	if err != nil {
		t.Fatalf("sync v1beta2 cluster queue: %v", err)
	}
	if !changed {
		t.Fatal("expected v1beta2 cluster queue quota to be updated")
	}

	updated, err := dynamic.Resource(installedGVR).Get(context.Background(), "cluster-gpu-queue", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get v1beta2 cluster queue: %v", err)
	}
	groups, _, _ := nestedSlice(updated.Object, "spec", "resourceGroups")
	group := groups[0].(map[string]any)
	flavors := group["flavors"].([]any)
	flavor := flavors[0].(map[string]any)
	resources := flavor["resources"].([]any)
	for _, item := range resources {
		resource := item.(map[string]any)
		if resource["name"] == "nvidia.com/gpu" && resource["nominalQuota"] != "24" {
			t.Fatalf("expected 24 GPU quota, got %#v", resource)
		}
	}
}
