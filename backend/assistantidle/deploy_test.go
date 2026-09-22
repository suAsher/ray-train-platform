package assistantidle

import (
	"os"
	"path/filepath"
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
	mustContain(t, files["localqueue.yaml"], "clusterQueue: cluster-gpu-queue")
	mustContain(t, files["workloadpriorityclass.yaml"], "kind: WorkloadPriorityClass")
	mustContain(t, files["workloadpriorityclass.yaml"], "value: -2000")

	for _, key := range []string{
		"\"QueueName\": \"assistant-idle-localqueue\"",
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
	for _, port := range []string{"port: 6379", "port: 8265", "port: 8000", "port: 8080", "port: 52365"} { mustNotContain(t,np,port) }

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
