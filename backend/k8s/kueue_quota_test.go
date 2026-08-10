package k8s

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func getOptions() metav1.GetOptions { return metav1.GetOptions{} }

func clusterQueueObject(name string, gpu, cpu, memory string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta1",
		"kind":       "ClusterQueue",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"resourceGroups": []any{map[string]any{
				"coveredResources": []any{"cpu", "memory", "nvidia.com/gpu"},
				"flavors": []any{map[string]any{
					"name": "gpu-flavor",
					"resources": []any{
						map[string]any{"name": "cpu", "nominalQuota": cpu},
						map[string]any{"name": "memory", "nominalQuota": memory},
						map[string]any{"name": "nvidia.com/gpu", "nominalQuota": gpu},
					},
				}},
			}},
		},
	}}
}

func quotaTestClient(existing *unstructured.Unstructured) (*Client, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{clusterQueueGVR: "ClusterQueueList"}, existing)
	return &Client{dynamic: dynamic}, dynamic
}

func nominalQuotaFor(t *testing.T, dynamic *dynamicfake.FakeDynamicClient, name, resourceName string) string {
	t.Helper()
	fetched, err := dynamic.Resource(clusterQueueGVR).Get(context.Background(), name, getOptions())
	if err != nil {
		t.Fatalf("get cluster queue: %v", err)
	}
	groups, _, _ := unstructured.NestedSlice(fetched.Object, "spec", "resourceGroups")
	group, _ := groups[0].(map[string]any)
	flavors, _ := group["flavors"].([]any)
	flavor, _ := flavors[0].(map[string]any)
	resources, _ := flavor["resources"].([]any)
	for _, item := range resources {
		entry, _ := item.(map[string]any)
		if entry["name"] == resourceName {
			return fmt.Sprint(entry["nominalQuota"])
		}
	}
	t.Fatalf("resource %q not found", resourceName)
	return ""
}

// The whole point: an operator labels a new machine and the admission budget
// follows, without anyone editing the ClusterQueue by hand.
func TestSyncClusterQueueQuotaFollowsPoolCapacity(t *testing.T) {
	client, dynamic := quotaTestClient(clusterQueueObject("cluster-gpu-queue", "1", "16", "64Gi"))
	capacity := TrainingPoolCapacity{Nodes: 3, GPUs: 24, CPUMillis: 192_000, MemoryBytes: 3 * 512 * 1024 * 1024 * 1024}

	changed, err := client.SyncClusterQueueQuota(context.Background(), "cluster-gpu-queue", capacity)
	if err != nil {
		t.Fatalf("sync quota: %v", err)
	}
	if !changed {
		t.Fatalf("expected the quota to be updated")
	}
	if got := nominalQuotaFor(t, dynamic, "cluster-gpu-queue", "nvidia.com/gpu"); got != "24" {
		t.Fatalf("expected 24 GPUs, got %q", got)
	}
	if got := nominalQuotaFor(t, dynamic, "cluster-gpu-queue", "cpu"); got != "192" {
		t.Fatalf("expected 192 cores, got %q", got)
	}
}

// Rewriting an unchanged object on every reconcile loop would churn the API
// server and the resource version for no reason.
func TestSyncClusterQueueQuotaIsNoOpWhenAlreadyCorrect(t *testing.T) {
	client, _ := quotaTestClient(clusterQueueObject("cluster-gpu-queue", "24", "192", "1536Gi"))
	capacity := TrainingPoolCapacity{Nodes: 3, GPUs: 24, CPUMillis: 192_000, MemoryBytes: 3 * 512 * 1024 * 1024 * 1024}

	changed, err := client.SyncClusterQueueQuota(context.Background(), "cluster-gpu-queue", capacity)
	if err != nil {
		t.Fatalf("sync quota: %v", err)
	}
	if changed {
		t.Fatalf("quota already matches capacity, no write expected")
	}
}

// Losing every training node must not silently wipe the queue: a zero budget
// would make every job hang in QUEUED with no explanation.
func TestSyncClusterQueueQuotaRefusesEmptyPool(t *testing.T) {
	client, dynamic := quotaTestClient(clusterQueueObject("cluster-gpu-queue", "24", "192", "1536Gi"))

	_, err := client.SyncClusterQueueQuota(context.Background(), "cluster-gpu-queue", TrainingPoolCapacity{})
	if err == nil {
		t.Fatalf("expected an error when no training node is available")
	}
	if got := nominalQuotaFor(t, dynamic, "cluster-gpu-queue", "nvidia.com/gpu"); got != "24" {
		t.Fatalf("existing quota must be left untouched, got %q", got)
	}
}
