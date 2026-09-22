package assistantidle

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"os"
	"path/filepath"
	"reflect"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"
)

func TestAssistantIdleDeployReferenceMatchesRendererContract(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "assistant-idle")
	files := map[string]string{}
	for _, name := range []string{
		"namespace.yaml",
		"config.example.json",
		"resourcequota.yaml",
		"localqueue.yaml",
		"workloadpriorityclass.yaml",
		"networkpolicy.yaml",
		"deployment-controller.yaml",
		"deployment-reaper.yaml",
		"rbac-controller.yaml",
	} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = string(body)
	}

	mustContain(t, files["namespace.yaml"], "app.kubernetes.io/part-of: ray-train-platform")
	mustContain(t, files["resourcequota.yaml"], "requests.nvidia.com/gpu: \"1\"")
	mustNotContain(t, files["resourcequota.yaml"], "limits.nvidia.com/gpu")
	mustContain(t, files["localqueue.yaml"], "clusterQueue: assistant-idle-private-cq")
	mustContain(t, files["workloadpriorityclass.yaml"], "kind: WorkloadPriorityClass")
	mustContain(t, files["workloadpriorityclass.yaml"], "value: -2000")

	for _, key := range []string{
		"\"QueueName\": \"assistant-idle-private-lq\"",
		"\"platform.wellspiking.ai/gpu-pool\": \"production\"",
		"\"platform.wellspiking.ai/cache-ready\": \"true\"",
		"\"accelerator\": \"nvidia-rtx-4090\"",
		"\"ToleratedTaintKeys\": [\"nvidia.com/gpu\"]",
		"\"ModelPath\": \"/models/Qwen3-8B-AWQ\"",
	} {
		mustContain(t, files["config.example.json"], key)
	}
	mustNotContain(t, files["config.example.json"], "platform.wellspiking.ai/dedicated-tenant")

	np := files["networkpolicy.yaml"]
	for _, key := range []string{
		"assistant-idle-default-deny",
		"assistant-idle-runtime-egress",
		"assistant-idle-controller-gate",
		"raytrain.wellspiking.ai/assistant-role: inference",
		"app: ray-train-backend",
		"app.kubernetes.io/component: api",
		"port: 8443",
	} {
		mustContain(t, np, key)
	}
	mustNotContain(t, np, "api-gateway")
	for _, port := range []string{"port: 6379", "port: 8265", "port: 8000", "port: 8080", "port: 52365"} {
		mustNotContain(t, np, port)
	}

	for _, name := range []string{"deployment-controller.yaml", "deployment-reaper.yaml"} {
		body := files[name]
		for _, key := range []string{"strategy:\n    type: Recreate", "resources:"} {
			mustContain(t, body, key)
		}
	}
	for _, key := range []string{"cpu: 100m", "memory: 128Mi", "cpu: \"2\"", "memory: 1Gi", "name: GOMEMLIMIT", "value: 700MiB"} {
		mustContain(t, files["deployment-controller.yaml"], key)
	}
	for _, key := range []string{"cpu: 50m", "memory: 64Mi", "cpu: 500m", "memory: 256Mi"} {
		mustContain(t, files["deployment-reaper.yaml"], key)
	}

	rbac := files["rbac-controller.yaml"]
	mustContain(t, rbac, "resources: [\"nodes\", \"pods\"]")
	mustContain(t, rbac, "resources: [\"rayjobs\", \"rayclusters\"]")
	mustContain(t, rbac, "resources: [\"workloads\"]")
	mustContain(t, rbac, "resources: [\"pods\"]")
	mustNotContain(t, rbac, "\"patch\", \"update\", \"delete\"]")
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("missing %q in:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("unexpected %q in:\n%s", needle, haystack)
	}
}

func TestAssistantPrivateQueueAccountsForInjectedENIWithoutSharedQuota(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "assistant-idle")
	read := func(name string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = yaml.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	cq := read("clusterqueue.yaml")
	if cq["kind"] != "ClusterQueue" || cq["metadata"].(map[string]any)["name"] != "assistant-idle-private-cq" {
		t.Fatal("unexpected cluster queue identity")
	}
	spec := cq["spec"].(map[string]any)
	wantSelector := map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "PLACEHOLDER_ASSISTANT_NAMESPACE"}}
	if !reflect.DeepEqual(spec["namespaceSelector"], wantSelector) {
		t.Fatal("queue must select only dedicated namespace")
	}
	if value, ok := spec["cohort"]; ok && value != "" {
		t.Fatal("must not borrow from training cohort")
	}
	wantPreemption := map[string]any{"withinClusterQueue": "Never", "reclaimWithinCohort": "Never", "borrowWithinCohort": map[string]any{"policy": "Never"}}
	if !reflect.DeepEqual(spec["preemption"], wantPreemption) {
		t.Fatal("preemption must remain disabled")
	}
	groups := spec["resourceGroups"].([]any)
	if len(groups) != 1 {
		t.Fatal("one resource group required")
	}
	group := groups[0].(map[string]any)
	want := map[string]string{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1", "vke.volcengine.com/eni-ip": "1"}
	covered := group["coveredResources"].([]any)
	if len(covered) != len(want) {
		t.Fatal("resource coverage mismatch")
	}
	for _, name := range covered {
		if _, ok := want[fmt.Sprint(name)]; !ok {
			t.Fatal("unexpected resource")
		}
	}
	flavors := group["flavors"].([]any)
	if len(flavors) != 1 {
		t.Fatal("one existing flavor required")
	}
	flavor := flavors[0].(map[string]any)
	if flavor["name"] != "gpu-4090-flavor" {
		t.Fatal("existing flavor must be reused")
	}
	resources := flavor["resources"].([]any)
	if len(resources) != len(want) {
		t.Fatal("quota count mismatch")
	}
	seen := map[string]bool{}
	for _, item := range resources {
		r := item.(map[string]any)
		name := fmt.Sprint(r["name"])
		expected, ok := want[name]
		if !ok || seen[name] {
			t.Fatal("unexpected or duplicate quota")
		}
		seen[name] = true
		actual, err := resource.ParseQuantity(fmt.Sprint(r["nominalQuota"]))
		if err != nil || actual.Cmp(resource.MustParse(expected)) != 0 {
			t.Fatalf("wrong quota for %s", name)
		}
		if len(r) != 2 {
			t.Fatal("no borrowing/lending overrides allowed")
		}
	}
	lq := read("localqueue.yaml")
	if lq["metadata"].(map[string]any)["name"] != "assistant-idle-private-lq" || lq["spec"].(map[string]any)["clusterQueue"] != "assistant-idle-private-cq" {
		t.Fatal("must create new LocalQueue, not mutate old immutable binding")
	}
	kustomization, err := os.ReadFile(filepath.Join(root, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(kustomization), "- clusterqueue.yaml")
	mustNotContain(t, string(kustomization), "cluster-gpu-queue")
	var kust map[string]any
	if err := yaml.Unmarshal(kustomization, &kust); err != nil {
		t.Fatal(err)
	}
	if _, ok := kust["namespace"]; ok {
		t.Fatal("global namespace transformer must not scope ClusterQueue")
	}
	generators := kust["configMapGenerator"].([]any)
	if generators[0].(map[string]any)["namespace"] != "PLACEHOLDER_ASSISTANT_NAMESPACE" {
		t.Fatal("generated ConfigMap must remain namespaced")
	}

}
