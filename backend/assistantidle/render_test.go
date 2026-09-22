package assistantidle

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func validRenderConfig() RenderConfig {
	return RenderConfig{
		Name:               "raytrain-assistant-idle",
		Namespace:          "raytrain-assistant-system",
		QueueName:          "assistant-idle-localqueue",
		ServeImage:         "harbor.wellspiking.ai/assistant/assistant-serve@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ModelPVC:           "qwen3-8b-awq-cache",
		ImagePullSecrets:   []string{"harbor-registry"},
		GateURL:            "http://assistant-idle-controller.raytrain-assistant-system.svc.cluster.local:8080/gate",
		AllowedWorkerNodes: []string{"gpu-node-a", "gpu-node-b"},
		InstanceID:         "assistant-idle-acceptance",
		ToleratedTaintKeys: []string{"nvidia.com/gpu"},
		RequiredNodeLabels: map[string]string{
			"platform.wellspiking.ai/gpu-pool":    "production",
			"platform.wellspiking.ai/cache-ready": "true",
			"accelerator":                         "nvidia-rtx-4090",
		},
	}
}

func renderForTest(t *testing.T, cfg RenderConfig) *unstructured.Unstructured {
	t.Helper()
	obj, err := RenderRayService(cfg)
	if err != nil {
		t.Fatalf("RenderRayService returned error: %v", err)
	}
	return obj
}

func TestRenderRayServiceValidatesImmutableInputs(t *testing.T) {
	base := validRenderConfig()
	for name, mutate := range map[string]func(*RenderConfig){
		"missing digest":        func(c *RenderConfig) { c.ServeImage = "harbor.wellspiking.ai/assistant/assistant-serve:latest" },
		"missing model pvc":     func(c *RenderConfig) { c.ModelPVC = "" },
		"unsafe pull secret":    func(c *RenderConfig) { c.ImagePullSecrets = []string{"harbor/registry"} },
		"duplicate pull secret": func(c *RenderConfig) { c.ImagePullSecrets = []string{"harbor-registry", "harbor-registry"} },
		"missing queue":         func(c *RenderConfig) { c.QueueName = "" },
		"missing allowed nodes": func(c *RenderConfig) { c.AllowedWorkerNodes = nil },
		"unsafe head nodes":     func(c *RenderConfig) { c.AllowedHeadNodes = []string{"head/node"} },
		"duplicate head nodes": func(c *RenderConfig) {
			c.AllowedHeadNodes = []string{"head-node-a", "head-node-a"}
		},
		"too many head nodes": func(c *RenderConfig) {
			c.AllowedHeadNodes = make([]string, 33)
			for i := range c.AllowedHeadNodes {
				c.AllowedHeadNodes[i] = fmt.Sprintf("head-node-%d", i)
			}
		},
		"unsafe namespace":           func(c *RenderConfig) { c.Namespace = "raytrain_assistant" },
		"dedicated taint toleration": func(c *RenderConfig) { c.ToleratedTaintKeys = []string{"platform.wellspiking.ai/dedicated-tenant"} },
		"dedicated label selector": func(c *RenderConfig) {
			c.RequiredNodeLabels = map[string]string{"platform.wellspiking.ai/dedicated-tenant": "tenant-a"}
		},
		"model path outside model mount": func(c *RenderConfig) { c.ModelPath = "/tmp/model" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := RenderRayService(cfg); err == nil {
				t.Fatal("accepted unsafe assistant idle render config")
			}
		})
	}
}

func TestRenderRayServiceUsesKueueSuspendedSingleGPUShape(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	if obj.GetAPIVersion() != "ray.io/v1" || obj.GetKind() != "RayService" {
		t.Fatalf("unexpected GVK %s %s", obj.GetAPIVersion(), obj.GetKind())
	}
	if obj.GetNamespace() != "raytrain-assistant-system" || obj.GetLabels()["kueue.x-k8s.io/queue-name"] != "assistant-idle-localqueue" || obj.GetLabels()["kueue.x-k8s.io/priority-class"] != "assistant-idle-low" || obj.GetLabels()["app.kubernetes.io/instance"] != "assistant-idle-acceptance" || obj.GetLabels()["app.kubernetes.io/component"] != "assistant-idle" {
		t.Fatalf("missing dedicated namespace, instance, component, or Kueue queue label: ns=%s labels=%v", obj.GetNamespace(), obj.GetLabels())
	}
	if _, found, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); found {
		t.Fatal("RayService must not render top-level spec.suspend; Kueue v0.19 reads rayClusterConfig.suspend")
	}
	if suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "rayClusterConfig", "suspend"); !suspended {
		t.Fatal("RayService must render spec.rayClusterConfig.suspend for Kueue v0.19 gating")
	}
	if strategy, _, _ := unstructured.NestedString(obj.Object, "spec", "upgradeStrategy", "type"); strategy != "None" {
		t.Fatalf("upgradeStrategy=%q, want None to avoid double RayCluster upgrades", strategy)
	}
	if rayVersion, _, _ := unstructured.NestedString(obj.Object, "spec", "rayClusterConfig", "rayVersion"); rayVersion != "2.58.0" {
		t.Fatalf("rayVersion=%q, want 2.58.0", rayVersion)
	}
	headLabels := asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "metadata", "labels"))
	if fmt.Sprint(headLabels["raytrain.wellspiking.ai/assistant-role"]) != "head" {
		t.Fatalf("head role label missing: %v", headLabels)
	}

	serveConfig := fmt.Sprint(at(t, obj.Object, "spec", "serveConfigV2"))
	if !strings.Contains(serveConfig, "proxy_location: HeadOnly") {
		t.Fatalf("Serve proxy must stay on head only, not EveryNode: %s", serveConfig)
	}

	workerGroups := asSlice(t, at(t, obj.Object, "spec", "rayClusterConfig", "workerGroupSpecs"))
	if len(workerGroups) != 1 {
		t.Fatalf("expected exactly one worker group: len=%d", len(workerGroups))
	}
	worker := asMap(t, workerGroups[0])
	workerLabels := asMap(t, at(t, worker, "template", "metadata", "labels"))
	if fmt.Sprint(workerLabels["raytrain.wellspiking.ai/assistant-role"]) != "worker" {
		t.Fatalf("worker role label missing: %v", workerLabels)
	}
	if worker["minReplicas"] != int64(1) || worker["maxReplicas"] != int64(1) || worker["replicas"] != int64(1) {
		t.Fatalf("worker group must be fixed at exactly one replica: %#v", worker)
	}
	resources := asMap(t, at(t, worker, "template", "spec", "containers", 0, "resources"))
	requests := stringMap(t, resources["requests"])
	if requests["nvidia.com/gpu"] != "1" || requests["cpu"] != "4" || requests["memory"] != "16Gi" {
		t.Fatalf("worker resource requests are not the fixed one-GPU shape: %v", requests)
	}
	limits := stringMap(t, resources["limits"])
	if limits["nvidia.com/gpu"] != "1" || limits["memory"] != "16Gi" {
		t.Fatalf("worker resource limits are not bounded: %v", limits)
	}
}

func TestRenderRayServicePropagatesImagePullSecretsToHeadAndWorker(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	headSecrets := asSlice(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "imagePullSecrets"))
	workerSecrets := asSlice(t, at(t, obj.Object, "spec", "rayClusterConfig", "workerGroupSpecs", 0, "template", "spec", "imagePullSecrets"))
	for name, secrets := range map[string][]any{"head": headSecrets, "worker": workerSecrets} {
		if len(secrets) != 1 || fmt.Sprint(asMap(t, secrets[0])["name"]) != "harbor-registry" {
			t.Fatalf("%s imagePullSecrets=%#v, want harbor-registry", name, secrets)
		}
	}
}

func TestRenderRayServiceUsesAgentHTTPProbesIndependentOfServeGate(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	head := asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "containers", 0))
	worker := asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "workerGroupSpecs", 0, "template", "spec", "containers", 0))
	for _, tc := range []struct {
		name      string
		container map[string]any
		probe     string
		path      string
	}{
		{"head readiness", head, "readinessProbe", "/api/healthz"},
		{"worker readiness", worker, "readinessProbe", "/api/local_raylet_healthz"},
		{"head liveness", head, "livenessProbe", "/api/healthz"},
		{"worker liveness", worker, "livenessProbe", "/api/healthz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := asMap(t, tc.container[tc.probe])
			if _, ok := probe["exec"]; ok {
				t.Fatal("probe must not depend on shell tools such as wget")
			}
			httpGet := asMap(t, probe["httpGet"])
			if httpGet["path"] != tc.path || httpGet["port"] != int64(52365) {
				t.Fatalf("probe must use the Ray agent, not Serve/gate: %#v", httpGet)
			}
			if host, ok := httpGet["host"]; ok && host != "" {
				t.Fatalf("kubelet HTTP probe must address Pod IP, not host %v", host)
			}
		})
	}
	if config := fmt.Sprint(at(t, obj.Object, "spec", "serveConfigV2")); !strings.Contains(config, "proxy_location: HeadOnly") {
		t.Fatal("worker probe fix must retain head-only Serve proxy")
	}
}

func TestRenderRayServiceBoundsHeadMemoryAndBothObjectStores(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	headResources := asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "containers", 0, "resources"))
	requests, limits := stringMap(t, headResources["requests"]), stringMap(t, headResources["limits"])
	if requests["memory"] != "4Gi" || limits["memory"] != "8Gi" || requests["cpu"] != "500m" || limits["cpu"] != "2" {
		t.Errorf("head needs 4Gi request/8Gi limit with unchanged CPU: requests=%v limits=%v", requests, limits)
	}
	for _, tc := range []struct {
		name   string
		params map[string]any
		bytes  string
	}{
		{"head", asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "rayStartParams")), "268435456"},
		{"worker", asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "workerGroupSpecs", 0, "rayStartParams")), "536870912"},
	} {
		if tc.params["object-store-memory"] != tc.bytes {
			t.Errorf("%s object-store-memory=%#v, want explicit byte string %s", tc.name, tc.params["object-store-memory"], tc.bytes)
		}
	}
}

func TestRenderRayServiceKeepsHeadCPUOnlyAndPinsHeadAwayFromDedicatedNodes(t *testing.T) {
	cfg := validRenderConfig()
	cfg.AllowedHeadNodes = []string{"shared-node-a"}
	obj := renderForTest(t, cfg)
	headResources := asMap(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "containers", 0, "resources"))
	headRequests := stringMap(t, headResources["requests"])
	if _, hasGPU := headRequests["nvidia.com/gpu"]; hasGPU {
		t.Fatalf("head group requests must be CPU-only: %v", headRequests)
	}
	headLimits := stringMap(t, headResources["limits"])
	if _, hasGPU := headLimits["nvidia.com/gpu"]; hasGPU {
		t.Fatalf("head group limits must be CPU-only: %v", headLimits)
	}
	if headRequests["cpu"] == "" || headRequests["memory"] == "" {
		t.Fatalf("head group must still request CPU and memory: %v", headRequests)
	}
	headTolerations := compact(at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "tolerations"))
	for _, want := range []string{"nvidia.com/gpu", "NoSchedule"} {
		if !strings.Contains(headTolerations, want) {
			t.Fatalf("head tolerations must retain GPU NoSchedule reachability, missing %q in %s", want, headTolerations)
		}
	}

	terms := asSlice(t, at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms"))
	if len(terms) != 1 {
		t.Fatalf("head must have a single explicit node affinity term: len=%d", len(terms))
	}
	joined := compact(asSlice(t, at(t, asMap(t, terms[0]), "matchExpressions")))
	for _, want := range []string{
		"kubernetes.io/hostname", "shared-node-a",
		"platform.wellspiking.ai/gpu-pool", "production",
		"platform.wellspiking.ai/cache-ready", "true",
		"accelerator", "nvidia-rtx-4090",
		"platform.wellspiking.ai/dedicated-tenant", "DoesNotExist",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("head affinity missing %q in %s", want, joined)
		}
	}
}

func TestRenderRayServiceDefaultsHeadNodesToWorkerNodesAndPinsWorkerToAllowedNodes(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	for _, tc := range []struct {
		name string
		path []any
	}{
		{"head", []any{"spec", "rayClusterConfig", "headGroupSpec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms"}},
		{"worker", []any{"spec", "rayClusterConfig", "workerGroupSpecs", 0, "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms"}},
	} {
		terms := asSlice(t, at(t, obj.Object, tc.path...))
		if len(terms) != 1 {
			t.Fatalf("%s must have a single explicit node affinity term: len=%d", tc.name, len(terms))
		}
		joined := compact(asSlice(t, at(t, asMap(t, terms[0]), "matchExpressions")))
		for _, want := range []string{
			"kubernetes.io/hostname", "gpu-node-a", "gpu-node-b",
			"platform.wellspiking.ai/gpu-pool", "production",
			"platform.wellspiking.ai/cache-ready", "true",
			"accelerator", "nvidia-rtx-4090",
			"platform.wellspiking.ai/dedicated-tenant", "DoesNotExist",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s affinity missing %q in %s", tc.name, want, joined)
			}
		}
	}
}

func TestRenderRayServiceMountsOnlyWorkerReadOnlyModelCacheAndOfflineEnvironment(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	encoded := compact(obj.Object)
	headEncoded := compact(at(t, obj.Object, "spec", "rayClusterConfig", "headGroupSpec", "template", "spec"))
	if strings.Contains(headEncoded, "model-cache") || strings.Contains(headEncoded, "persistentVolumeClaim") {
		t.Fatalf("head pod must not mount the model PVC, to avoid RWO multi-node conflicts: %s", headEncoded)
	}
	workerEncoded := compact(at(t, obj.Object, "spec", "rayClusterConfig", "workerGroupSpecs", 0, "template", "spec"))
	if !strings.Contains(workerEncoded, "model-cache") || !strings.Contains(workerEncoded, "persistentVolumeClaim") {
		t.Fatalf("worker pod must mount the model PVC read-only: %s", workerEncoded)
	}
	for _, mustContain := range []string{
		"qwen3-8b-awq-cache", "persistentVolumeClaim", "/models", "readOnly:true",
		"HF_HUB_OFFLINE", "TRANSFORMERS_OFFLINE", "HOME", "/home/assistant", "HF_HOME", "/tmp/huggingface", "XDG_CACHE_HOME", "/tmp/.cache", "VLLM_CACHE_ROOT", "/tmp/vllm-cache", "ASSISTANT_MODEL_PATH", "/models/Qwen3-8B-AWQ", "ASSISTANT_GATE_URL",
		"http://assistant-idle-controller.raytrain-assistant-system.svc.cluster.local:8080/gate",
	} {
		if !strings.Contains(encoded, mustContain) {
			t.Fatalf("rendered RayService missing %q in %s", mustContain, encoded)
		}
	}
	for _, forbidden := range []string{"/mnt/storage", "personal", "hostPath", "NodePort", "LoadBalancer"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("rendered RayService leaked forbidden storage or exposure %q in %s", forbidden, encoded)
		}
	}
}

func TestRenderRayServiceHardensPodsWithoutServiceAccountTokensOrPreemption(t *testing.T) {
	obj := renderForTest(t, validRenderConfig())
	encoded := compact(obj.Object)
	for _, mustContain := range []string{
		"automountServiceAccountToken:false", "preemptionPolicy:Never", "priorityClassName:assistant-idle-low",
		"terminationGracePeriodSeconds:15", "hostIPC:false", "runAsUser:1000", "readOnlyRootFilesystem:true",
		"emptyDir", "dev-shm", "sizeLimit:8Gi", "/tmp", "/home/assistant",
		"serviceType:ClusterIP", "dashboard-host", "0.0.0.0", "include-dashboard", "true",
		"object-manager-port", "8076", "dashboard-agent-grpc-port", "52366", "runtime-env-agent-port", "52367", "min-worker-port", "10002", "max-worker-port", "10032",
		"tolerations", "nvidia.com/gpu", "NoSchedule",
	} {
		if !strings.Contains(encoded, mustContain) {
			t.Fatalf("rendered RayService missing hardening marker %q in %s", mustContain, encoded)
		}
	}
}

func at(t *testing.T, value any, path ...any) any {
	t.Helper()
	current := value
	for _, segment := range path {
		switch key := segment.(type) {
		case string:
			m := asMap(t, current)
			current = m[key]
		case int:
			s := asSlice(t, current)
			if key < 0 || key >= len(s) {
				t.Fatalf("index %d out of range in %v", key, path)
			}
			current = s[key]
		default:
			t.Fatalf("unsupported path segment %T", segment)
		}
	}
	return current
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	m, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T: %#v", value, value)
	}
	return m
}

func asSlice(t *testing.T, value any) []any {
	t.Helper()
	s, ok := value.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T: %#v", value, value)
	}
	return s
}

func stringMap(t *testing.T, value any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range asMap(t, value) {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func compact(value any) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(fmt.Sprintf("%#v", value)), ""), "\"", "")
}
