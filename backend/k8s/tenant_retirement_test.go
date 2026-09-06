package k8s

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func retirementClient(objects ...runtime.Object) *Client {
	kinds := map[schema.GroupVersionResource]string{}
	for _, resource := range tenantRetirementResources(localQueueGVR) {
		kinds[resource.gvr] = resource.kind + "List"
	}
	return NewClientFromInterfaces(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...), nil)
}

func TestTenantRetirementKubernetesTerminalAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		phase string
		want  int
	}{{"Succeeded", 0}, {"Failed", 0}, {"Running", 1}, {"Pending", 1}, {"", 1}} {
		t.Run(tc.phase, func(t *testing.T) {
			pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "fixture", "namespace": "tenant-test"}, "status": map[string]any{"phase": tc.phase}}}
			client := retirementClient(pod)
			blockers, err := client.TenantRetirementBlockers(context.Background(), "tenant-test")
			if err != nil || len(blockers) != tc.want {
				t.Fatalf("blockers=%v err=%v", blockers, err)
			}
			for _, action := range client.dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
				if action.GetVerb() != "list" {
					t.Fatalf("unexpected mutation %s", action.GetVerb())
				}
			}
		})
	}
}

func TestTenantRetirementKubernetesFailsClosed(t *testing.T) {
	client := retirementClient()
	client.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "rayjobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	if _, err := client.TenantRetirementBlockers(context.Background(), "tenant-test"); err == nil {
		t.Fatal("must reject unavailable inventory")
	}
	if _, err := client.TenantRetirementBlockers(context.Background(), ""); err == nil {
		t.Fatal("must not list all namespaces")
	}
}

func TestTenantRetirementKubernetesScheduledWorkBlocks(t *testing.T) {
	job := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "job", "namespace": "tenant-test"}}}
	client := retirementClient(job)
	blockers, err := client.TenantRetirementBlockers(context.Background(), "tenant-test")
	if err != nil || len(blockers) != 1 {
		t.Fatalf("%v %v", blockers, err)
	}
	job.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}
	_, err = client.dynamic.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("tenant-test").Update(context.Background(), job, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	blockers, err = client.TenantRetirementBlockers(context.Background(), "tenant-test")
	if err != nil || len(blockers) != 0 {
		t.Fatalf("%v %v", blockers, err)
	}
}

func TestTenantRetirementResourceStates(t *testing.T) {
	for _, tc := range []struct {
		kind, state string
		active      bool
	}{
		{"RayJob", "Complete", false}, {"RayJob", "Failed", false},
		{"RayJob", "Running", true}, {"RayJob", "", true},
		{"Workload", "Finished", false}, {"Workload", "Admitted", true},
		{"Job", "Failed", false}, {"Job", "Suspended", true},
		{"RayCluster", "", true}, {"Deployment", "", true},
		{"CronJob", "", true}, {"StatefulSet", "", true}, {"DaemonSet", "", true},
	} {
		t.Run(tc.kind+tc.state, func(t *testing.T) {
			object := map[string]any{"status": map[string]any{
				"jobDeploymentStatus": tc.state,
				"conditions":          []any{map[string]any{"type": tc.state, "status": "True"}},
			}}
			if got := retirementResourceActive(tc.kind, object); got != tc.active {
				t.Fatalf("active=%v want %v", got, tc.active)
			}
		})
	}
}

func TestTenantRetirementKubernetesNamespaceIsolation(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "running", "namespace": "other-tenant"}, "status": map[string]any{"phase": "Running"}}}
	client := retirementClient(pod)
	blockers, err := client.TenantRetirementBlockers(context.Background(), "tenant-test")
	if err != nil || len(blockers) != 0 {
		t.Fatalf("other tenant affected inventory: %v %v", blockers, err)
	}
	for _, action := range client.dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
		if action.GetNamespace() != "tenant-test" || action.GetVerb() != "list" {
			t.Fatalf("unsafe action: %v", action)
		}
	}
}
