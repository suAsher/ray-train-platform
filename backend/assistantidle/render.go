package assistantidle

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	defaultRayVersion           = "2.58.0"
	defaultPriorityClassName    = "assistant-idle-low"
	defaultModelMountPath       = "/models"
	defaultModelPath            = "/models/Qwen3-8B-AWQ"
	defaultServeImportPath      = "assistant_serve.app:deployment"
	queueNameLabel              = "kueue.x-k8s.io/queue-name"
	workloadPriorityClassLabel  = "kueue.x-k8s.io/priority-class"
	workerGroupName             = "gpu-worker"
	dedicatedTenantKey          = "platform.wellspiking.ai/dedicated-tenant"
	defaultGPUDeviceTaintKey    = "nvidia.com/gpu"
	rayObjectManagerPort        = "8076"
	rayNodeManagerPort          = "8077"
	rayDashboardAgentListenPort = "52365"
	rayDashboardAgentGRPCPort   = "52366"
	rayRuntimeEnvAgentPort      = "52367"
	rayMinWorkerPort            = "10002"
	rayMaxWorkerPort            = "10032"
)

var (
	dnsLabelPattern     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	imageDigestPattern  = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)
	labelKeySafePattern = regexp.MustCompile(`^([A-Za-z0-9][-A-Za-z0-9_.]*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
)

type RenderConfig struct {
	RuntimeType               string
	TLSSecretName             string
	AuthSecretName            string
	GateCASecretName          string
	Name                      string
	Namespace                 string
	QueueName                 string
	ServeImage                string
	ModelPVC                  string
	ImagePullSecrets          []string
	GateURL                   string
	AllowedWorkerNodes        []string
	AllowedHeadNodes          []string
	RequiredNodeLabels        map[string]string
	ToleratedTaintKeys        []string
	InstanceID                string
	RayVersion                string
	PriorityClassName         string
	WorkloadPriorityClassName string
	ModelMountPath            string
	ModelPath                 string
	ServeImportPath           string
}

func RenderRayService(cfg RenderConfig) (*unstructured.Unstructured, error) {
	cfg = cfg.withDefaults()
	if err := validateRenderConfig(cfg); err != nil {
		return nil, err
	}

	labels := map[string]string{
		"app.kubernetes.io/name":    cfg.Name,
		"app.kubernetes.io/part-of": "raytrain-assistant-idle",
		componentLabel:              componentValue,
		instanceLabel:               cfg.InstanceID,
		queueNameLabel:              cfg.QueueName,
		workloadPriorityClassLabel:  cfg.WorkloadPriorityClassName,
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ray.io/v1",
		"kind":       "RayService",
		"metadata": map[string]any{
			"name":      cfg.Name,
			"namespace": cfg.Namespace,
			"labels":    stringMapAny(labels),
		},
		"spec": map[string]any{
			"upgradeStrategy": map[string]any{"type": "None"},
			"serveConfigV2":   serveConfig(cfg),
			"rayClusterConfig": map[string]any{
				// Kueue v0.19 RayService integration reads this nested suspend flag.
				// The controller must reject startup if the live RayService CRD does not admit it.
				"suspend":                 true,
				"rayVersion":              cfg.RayVersion,
				"enableInTreeAutoscaling": false,
				"headGroupSpec": map[string]any{
					"serviceType":    "ClusterIP",
					"rayStartParams": headRayStartParams(),
					"template": map[string]any{
						"metadata": map[string]any{"labels": stringMapAny(podLabels(labels, "head"))},
						"spec":     podSpec(cfg, false),
					},
				},
				"workerGroupSpecs": []any{
					map[string]any{
						"groupName":      workerGroupName,
						"replicas":       int64(1),
						"minReplicas":    int64(1),
						"maxReplicas":    int64(1),
						"rayStartParams": workerRayStartParams(),
						"template": map[string]any{
							"metadata": map[string]any{"labels": stringMapAny(podLabels(labels, "worker"))},
							"spec":     podSpec(cfg, true),
						},
					},
				},
			},
		},
	}}
	return obj, nil
}

func (cfg RenderConfig) withDefaults() RenderConfig {
	if cfg.InstanceID == "" {
		cfg.InstanceID = cfg.Name
	}
	if cfg.RayVersion == "" {
		cfg.RayVersion = defaultRayVersion
	}
	if cfg.PriorityClassName == "" {
		cfg.PriorityClassName = defaultPriorityClassName
	}
	if cfg.ModelMountPath == "" {
		cfg.ModelMountPath = defaultModelMountPath
	}
	if cfg.ModelPath == "" {
		cfg.ModelPath = defaultModelPath
	}
	if cfg.WorkloadPriorityClassName == "" {
		cfg.WorkloadPriorityClassName = cfg.PriorityClassName
	}
	if cfg.ServeImportPath == "" {
		cfg.ServeImportPath = defaultServeImportPath
	}
	if len(cfg.AllowedHeadNodes) == 0 {
		cfg.AllowedHeadNodes = append([]string(nil), cfg.AllowedWorkerNodes...)
	}
	if len(cfg.ToleratedTaintKeys) == 0 {
		cfg.ToleratedTaintKeys = []string{defaultGPUDeviceTaintKey}
	}
	return cfg
}

func validateRenderConfig(cfg RenderConfig) error {
	for _, item := range []struct{ name, value string }{
		{"name", cfg.Name}, {"namespace", cfg.Namespace}, {"queue", cfg.QueueName}, {"model pvc", cfg.ModelPVC}, {"priority class", cfg.PriorityClassName}, {"workload priority class", cfg.WorkloadPriorityClassName}, {"instance", cfg.InstanceID},
	} {
		if !dnsLabelPattern.MatchString(item.value) || len(item.value) > 63 {
			return errors.New("assistant idle render requires safe DNS label values")
		}
	}
	if !imageDigestPattern.MatchString(cfg.ServeImage) {
		return errors.New("assistant idle serve image must be pinned by sha256 digest")
	}
	if len(cfg.ImagePullSecrets) > 8 {
		return errors.New("assistant idle image pull secret list is too large")
	}
	seenPullSecrets := map[string]bool{}
	for _, name := range cfg.ImagePullSecrets {
		if !dnsLabelPattern.MatchString(name) || len(name) > 63 || seenPullSecrets[name] {
			return errors.New("assistant idle image pull secret name is invalid")
		}
		seenPullSecrets[name] = true
	}
	if err := validateNodeList(cfg.AllowedWorkerNodes, "worker"); err != nil {
		return err
	}
	if err := validateNodeList(cfg.AllowedHeadNodes, "head"); err != nil {
		return err
	}
	if len(cfg.RequiredNodeLabels) == 0 || len(cfg.RequiredNodeLabels) > 16 {
		return errors.New("assistant idle render requires bounded node labels")
	}
	for key, value := range cfg.RequiredNodeLabels {
		if !labelKeySafePattern.MatchString(key) || !safeLabelValue(value) || key == dedicatedTenantKey {
			return errors.New("assistant idle required node label is invalid")
		}
	}
	if len(cfg.ToleratedTaintKeys) > 16 {
		return errors.New("assistant idle render has too many tolerated taints")
	}
	seenTaints := map[string]bool{}
	for _, key := range cfg.ToleratedTaintKeys {
		if !labelKeySafePattern.MatchString(key) || seenTaints[key] || key == dedicatedTenantKey {
			return errors.New("assistant idle tolerated taint key is invalid")
		}
		seenTaints[key] = true
	}
	if cfg.ModelMountPath != defaultModelMountPath {
		return errors.New("assistant idle model cache must mount at /models")
	}
	if !strings.HasPrefix(cfg.ModelPath, cfg.ModelMountPath+"/") || containsControl(cfg.ModelPath) || strings.Contains(cfg.ModelPath, "..") {
		return errors.New("assistant idle model path must be a fixed subdirectory under /models")
	}
	if strings.Contains(cfg.ServeImportPath, " ") || containsControl(cfg.ServeImportPath) || cfg.ServeImportPath == "" {
		return errors.New("assistant idle serve import path is invalid")
	}
	u, err := url.Parse(cfg.GateURL)
	scheme := "http"
	if cfg.RuntimeType == "pod" {
		scheme = "https"
	}
	if err != nil || u.Scheme != scheme || u.Host == "" || strings.Contains(cfg.GateURL, "@") || u.RawQuery != "" || u.Fragment != "" || containsControl(cfg.GateURL) {
		return errors.New("assistant idle gate URL must be a fixed in-cluster HTTP URL without credentials, query, or fragment")
	}
	if strings.Contains(cfg.GateURL, "PLACEHOLDER") || strings.Contains(cfg.GateURL, "<") || strings.Contains(cfg.GateURL, ">") {
		return errors.New("assistant idle render config contains an unresolved placeholder")
	}
	return nil
}

func podSpec(cfg RenderConfig, worker bool) map[string]any {
	volumeMounts := []any{
		map[string]any{"name": "tmp", "mountPath": "/tmp"},
		map[string]any{"name": "dev-shm", "mountPath": "/dev/shm"},
		map[string]any{"name": "home", "mountPath": "/home/assistant"},
	}
	volumes := []any{
		map[string]any{"name": "tmp", "emptyDir": map[string]any{}},
		map[string]any{"name": "dev-shm", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "8Gi"}},
		map[string]any{"name": "home", "emptyDir": map[string]any{}},
	}
	if worker {
		volumeMounts = append(volumeMounts, map[string]any{"name": "model-cache", "mountPath": cfg.ModelMountPath, "readOnly": true})
		volumes = append(volumes, map[string]any{
			"name":                  "model-cache",
			"persistentVolumeClaim": map[string]any{"claimName": cfg.ModelPVC, "readOnly": true},
		})
	}
	readinessPath := "/api/healthz"
	if worker {
		readinessPath = "/api/local_raylet_healthz"
	}
	container := map[string]any{
		"name":            "assistant-serve",
		"image":           cfg.ServeImage,
		"imagePullPolicy": "IfNotPresent",
		"env":             serveEnv(cfg),
		"volumeMounts":    volumeMounts,
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"readOnlyRootFilesystem":   true,
			"runAsNonRoot":             true,
			"runAsUser":                int64(1000),
			"runAsGroup":               int64(1000),
			"capabilities":             map[string]any{"drop": []any{"ALL"}},
		},
		"resources":      resources(worker),
		"readinessProbe": rayHealthProbe(readinessPath, false),
		"livenessProbe":  rayHealthProbe("/api/healthz", true),
	}
	spec := map[string]any{
		"automountServiceAccountToken":  false,
		"priorityClassName":             cfg.PriorityClassName,
		"preemptionPolicy":              "Never",
		"terminationGracePeriodSeconds": int64(15),
		"enableServiceLinks":            false,
		"hostIPC":                       false,
		"securityContext":               map[string]any{"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers":                    []any{container},
		"volumes":                       volumes,
	}
	if len(cfg.ImagePullSecrets) > 0 {
		spec["imagePullSecrets"] = imagePullSecrets(cfg.ImagePullSecrets)
	}
	if worker {
		spec["affinity"] = workerAffinity(cfg)
	} else {
		spec["affinity"] = headAffinity(cfg)
	}
	spec["tolerations"] = gpuTolerations(cfg)
	return spec
}

func rayHealthProbe(path string, liveness bool) map[string]any {
	// Kubelet calls the agent directly: no wget dependency or worker Serve proxy.
	// Serve readiness remains a RayService condition and never depends on gate.
	probe := map[string]any{
		"httpGet":             map[string]any{"path": path, "port": int64(52365)},
		"initialDelaySeconds": int64(10),
		"periodSeconds":       int64(5),
		"timeoutSeconds":      int64(2),
		"failureThreshold":    int64(3),
	}
	if liveness {
		probe["initialDelaySeconds"] = int64(60)
		probe["periodSeconds"] = int64(10)
		probe["failureThreshold"] = int64(6)
	}
	return probe
}

func resources(worker bool) map[string]any {
	if worker {
		return map[string]any{
			"requests": map[string]any{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1"},
			"limits":   map[string]any{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1"},
		}
	}
	return map[string]any{
		"requests": map[string]any{"cpu": "500m", "memory": "4Gi"},
		"limits":   map[string]any{"cpu": "2", "memory": "8Gi"},
	}
}

func serveEnv(cfg RenderConfig) []any {
	return []any{
		map[string]any{"name": "HF_HUB_OFFLINE", "value": "1"},
		map[string]any{"name": "TRANSFORMERS_OFFLINE", "value": "1"},
		map[string]any{"name": "HOME", "value": "/home/assistant"},
		map[string]any{"name": "HF_HOME", "value": "/tmp/huggingface"},
		map[string]any{"name": "XDG_CACHE_HOME", "value": "/tmp/.cache"},
		map[string]any{"name": "VLLM_CACHE_ROOT", "value": "/tmp/vllm-cache"},
		map[string]any{"name": "VLLM_NO_USAGE_STATS", "value": "1"},
		map[string]any{"name": "ASSISTANT_GATE_URL", "value": cfg.GateURL},
		map[string]any{"name": "ASSISTANT_MODEL_PATH", "value": cfg.ModelPath},
	}
}

func headAffinity(cfg RenderConfig) map[string]any {
	return nodeAffinity(cfg, cfg.AllowedHeadNodes)
}

func workerAffinity(cfg RenderConfig) map[string]any {
	return nodeAffinity(cfg, cfg.AllowedWorkerNodes)
}

func nodeAffinity(cfg RenderConfig, allowedNodes []string) map[string]any {
	expressions := []any{
		map[string]any{"key": "kubernetes.io/hostname", "operator": "In", "values": stringSlice(allowedNodes)},
		map[string]any{"key": dedicatedTenantKey, "operator": "DoesNotExist"},
	}
	keys := make([]string, 0, len(cfg.RequiredNodeLabels))
	for key := range cfg.RequiredNodeLabels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		expressions = append(expressions, map[string]any{"key": key, "operator": "In", "values": []any{cfg.RequiredNodeLabels[key]}})
	}
	return map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": expressions}}}}}
}

func headRayStartParams() map[string]any {
	params := commonRayStartParams()
	params["dashboard-host"] = "0.0.0.0"
	params["include-dashboard"] = "true"
	params["num-cpus"] = "0"
	params["object-store-memory"] = "268435456"
	return params
}

func workerRayStartParams() map[string]any {
	params := commonRayStartParams()
	params["num-gpus"] = "1"
	params["object-store-memory"] = "536870912"
	return params
}

func commonRayStartParams() map[string]any {
	return map[string]any{
		"object-manager-port":         rayObjectManagerPort,
		"node-manager-port":           rayNodeManagerPort,
		"dashboard-agent-listen-port": rayDashboardAgentListenPort,
		"dashboard-agent-grpc-port":   rayDashboardAgentGRPCPort,
		"runtime-env-agent-port":      rayRuntimeEnvAgentPort,
		"min-worker-port":             rayMinWorkerPort,
		"max-worker-port":             rayMaxWorkerPort,
	}
}

func gpuTolerations(cfg RenderConfig) []any {
	keys := append([]string(nil), cfg.ToleratedTaintKeys...)
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"key": key, "operator": "Exists", "effect": "NoSchedule"})
	}
	return out
}

func serveConfig(cfg RenderConfig) string {
	return strings.Join([]string{
		"proxy_location: HeadOnly",
		"applications:",
		"- name: raytrain-assistant",
		"  route_prefix: /",
		"  import_path: " + cfg.ServeImportPath,
		"  runtime_env:",
		"    env_vars:",
		"      HF_HUB_OFFLINE: \"1\"",
		"      TRANSFORMERS_OFFLINE: \"1\"",
		"      HOME: \"/home/assistant\"",
		"      HF_HOME: \"/tmp/huggingface\"",
		"      XDG_CACHE_HOME: \"/tmp/.cache\"",
		"      VLLM_CACHE_ROOT: \"/tmp/vllm-cache\"",
		"      ASSISTANT_MODEL_PATH: \"" + cfg.ModelPath + "\"",
		"      ASSISTANT_GATE_URL: \"" + cfg.GateURL + "\"",
		"  deployments:",
		"  - name: AssistantDeployment",
		"    num_replicas: 1",
		"    ray_actor_options:",
		"      num_gpus: 1",
		"      num_cpus: 4",
	}, "\n") + "\n"
}

func podLabels(base map[string]string, role string) map[string]string {
	labels := make(map[string]string, len(base)+1)
	for key, value := range base {
		labels[key] = value
	}
	labels["raytrain.wellspiking.ai/assistant-role"] = role
	return labels
}

func stringMapAny(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringSlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func validateNodeList(nodes []string, role string) error {
	if len(nodes) == 0 || len(nodes) > 32 {
		return errors.New("assistant idle render requires an explicit bounded " + role + " node allowlist")
	}
	seenNodes := map[string]bool{}
	for _, node := range nodes {
		if !safeName(node) || seenNodes[node] {
			return errors.New("assistant idle " + role + " node allowlist is invalid")
		}
		seenNodes[node] = true
	}
	return nil
}

func safeName(value string) bool {
	return value != "" && len(value) <= 253 && !strings.ContainsAny(value, "/@") && !containsControl(value)
}

func safeLabelValue(value string) bool {
	return value != "" && len(value) <= 63 && !containsControl(value) && !strings.ContainsAny(value, "/@")
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func imagePullSecrets(names []string) []any {
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"name": name})
	}
	return out
}
