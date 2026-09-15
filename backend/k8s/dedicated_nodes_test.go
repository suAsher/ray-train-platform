package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"ray-train-platform-backend/domain"
)

func checkDedicatedPlacement(t *testing.T, spec map[string]any, owner bool) {
	t.Helper()
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(map[string]any{"spec": spec}, &pod); err != nil {
		t.Fatal(err)
	}
	for _, hostname := range []string{"172.28.3.32", "172.28.1.229"} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: hostname, Labels: map[string]string{"kubernetes.io/hostname": hostname, "accelerator": "nvidia-rtx-4090"}}}
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil || pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			t.Fatal("missing hard node restriction")
		}
		matches := false
		for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			selector := &metav1.LabelSelector{}
			for _, requirement := range term.MatchExpressions {
				selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{Key: requirement.Key, Operator: metav1.LabelSelectorOperator(requirement.Operator), Values: requirement.Values})
			}
			parsed, err := metav1.LabelSelectorAsSelector(selector)
			if err != nil {
				t.Fatal(err)
			}
			matches = matches || parsed.Matches(labels.Set(node.Labels))
		}
		want := (hostname == "172.28.3.32") == owner
		if matches != want {
			t.Fatalf("owner=%t node=%s matches=%t want=%t", owner, hostname, matches, want)
		}
	}
	found := false
	for _, tol := range pod.Spec.Tolerations {
		if tol.Key == DedicatedTenantTaintKey {
			found = tol.Operator == corev1.TolerationOpEqual && tol.Value == "algorithm" && tol.Effect == corev1.TaintEffectNoSchedule
		}
	}
	if found != owner {
		t.Fatalf("dedicated taint toleration owner=%t: %+v", owner, pod.Spec.Tolerations)
	}
}

func TestDedicatedNodesConstrainEveryTrainingPod(t *testing.T) {
	for _, tenant := range []string{"algorithm", "local"} {
		for _, tas := range []bool{false, true} {
			job := validRenderJob()
			job.TenantID = tenant
			options := testRenderOptions()
			options.DedicatedNodes = map[string][]string{"algorithm": {"172.28.3.32"}}
			options.TopologyAwareScheduling = tas
			options.TeamNodeAffinityEnabled = true
			options.TeamNodeAffinityPreference = []string{"172.28.1.229"}
			manifest, err := RenderRayJob(job, options)
			if err != nil {
				t.Fatal(err)
			}
			submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
			head, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
			workers, _, _ := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
			worker := workers[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			for _, spec := range []map[string]any{submitter, head, worker} {
				checkDedicatedPlacement(t, spec, tenant == "algorithm")
			}
		}
	}
}

func TestDedicatedNodesConstrainGPUAndCPUWorkspaces(t *testing.T) {
	for _, tenant := range []string{"algorithm", "local"} {
		for _, gpus := range []int{0, 1, 8} {
			workspace := domain.DevWorkspace{Name: "debug-test", Namespace: "tenant-test", TenantID: tenant, GPUCount: gpus}
			manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: validRenderJob().Spec.Image, DedicatedNodes: map[string][]string{"algorithm": {"172.28.3.32"}}})
			if err != nil {
				t.Fatal(err)
			}
			head, _, _ := nestedMap(manifest.Object, "spec", "headGroupSpec", "template", "spec")
			workers, _, _ := nestedSlice(manifest.Object, "spec", "workerGroupSpecs")
			worker := workers[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			for _, spec := range []map[string]any{head, worker} {
				checkDedicatedPlacement(t, spec, tenant == "algorithm")
			}
		}
	}
}

func TestDedicatedNodesLeaveExistingRayJobUntouched(t *testing.T) {
	job := validRenderJob()
	job.TenantID = "algorithm"
	existing, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	existing.SetUID("existing-e-zone")
	clientset := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	client := NewClientFromInterfaces(clientset, nil)
	options := testRenderOptions()
	options.DedicatedNodes = map[string][]string{"algorithm": {"172.28.3.32"}}
	candidate, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := client.EnsureRayJob(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if actual.GetUID() != existing.GetUID() {
		t.Fatal("existing RayJob was replaced")
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("unexpected write to existing RayJob: %s", action.GetVerb())
		}
	}
}

func TestDedicatedNodesDoNotMutateConfiguration(t *testing.T) {
	assignments := map[string][]string{"algorithm": {"node-z", "node-a"}, "team-b": {"node-b"}}
	_ = dedicatedNodeAffinity("algorithm", assignments)
	_ = dedicatedNodeAffinity("local", assignments)
	if assignments["algorithm"][0] != "node-z" || len(assignments["algorithm"]) != 2 {
		t.Fatal("policy was mutated")
	}
	if dedicatedNodeAffinity("local", nil) != nil || len(dedicatedNodeTolerations("local", assignments)) != 0 {
		t.Fatal("shared policy changed")
	}
}
