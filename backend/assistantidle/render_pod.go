package assistantidle

import (
	"errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/url"
	"strings"
)

const podAdmissionGate = "kueue.x-k8s.io/admission"

func renderRuntime(cfg RenderConfig) (*unstructured.Unstructured, error) {
	if cfg.RuntimeType == "pod" {
		return RenderPod(cfg)
	}
	return RenderRayService(cfg)
}

// RenderPod creates an inert, Kueue-gated Pod. Only Kueue may remove its gate.
func RenderPod(cfg RenderConfig) (*unstructured.Unstructured, error) {
	cfg = cfg.withDefaults()
	cfg.RuntimeType = "pod"
	if err := validateRenderConfig(cfg); err != nil {
		return nil, err
	}
	for _, name := range []string{cfg.TLSSecretName, cfg.AuthSecretName, cfg.GateCASecretName} {
		if len(name) > 63 || !dnsLabelPattern.MatchString(name) {
			return nil, errors.New("plain inference requires existing TLS, auth and gate CA Secrets")
		}
	}
	gate, err := url.Parse(cfg.GateURL)
	if err != nil || !strings.HasSuffix(gate.Hostname(), ".svc.cluster.local") || gate.Port() != "8443" || gate.Path != "/gate" {
		return nil, errors.New("plain inference gate must use the fixed in-cluster HTTPS gate on 8443")
	}
	labels := map[string]string{
		"app.kubernetes.io/name": cfg.Name, "app.kubernetes.io/part-of": "raytrain-assistant-idle", componentLabel: componentValue, instanceLabel: cfg.InstanceID,
		queueNameLabel: cfg.QueueName, workloadPriorityClassLabel: cfg.WorkloadPriorityClassName, "kueue.x-k8s.io/managed": "true", "raytrain.wellspiking.ai/assistant-role": "inference",
	}
	spec := podSpec(cfg, true)
	spec["restartPolicy"] = "Never"
	spec["activeDeadlineSeconds"] = int64(3600)
	spec["schedulingGates"] = []any{map[string]any{"name": podAdmissionGate}}
	container := spec["containers"].([]any)[0].(map[string]any)
	container["command"] = []any{"python3", "-m", "assistant_serve.standalone"}
	container["ports"] = []any{map[string]any{"name": "https", "containerPort": int64(8443)}}
	probe := map[string]any{"httpGet": map[string]any{"scheme": "HTTPS", "path": "/livez", "port": int64(8443)}, "periodSeconds": int64(5), "timeoutSeconds": int64(2), "failureThreshold": int64(3)}
	container["readinessProbe"] = probe
	delete(container, "livenessProbe") // Controller start timeout handles model boot failure without restarting GPU work.
	mounts := container["volumeMounts"].([]any)
	volumes := spec["volumes"].([]any)
	for _, mount := range []struct{ name, secret, path string }{
		{"tls", cfg.TLSSecretName, "/run/assistant/tls"}, {"auth", cfg.AuthSecretName, "/run/assistant/auth"}, {"gate-ca", cfg.GateCASecretName, "/run/assistant/gate-ca"},
	} {
		mounts = append(mounts, map[string]any{"name": mount.name, "mountPath": mount.path, "readOnly": true})
		volumes = append(volumes, map[string]any{"name": mount.name, "secret": map[string]any{"secretName": mount.secret, "defaultMode": int64(0440)}})
	}
	container["volumeMounts"] = mounts
	spec["volumes"] = volumes
	spec["securityContext"].(map[string]any)["fsGroup"] = int64(1000)
	env := container["env"].([]any)
	for _, v := range []struct{ name, path string }{
		{"ASSISTANT_TLS_CERT_FILE", "/run/assistant/tls/tls.crt"}, {"ASSISTANT_TLS_KEY_FILE", "/run/assistant/tls/tls.key"},
		{"ASSISTANT_AUTH_TOKEN_FILE", "/run/assistant/auth/token"}, {"ASSISTANT_GATE_CA_FILE", "/run/assistant/gate-ca/ca.crt"},
	} {
		env = append(env, map[string]any{"name": v.name, "value": v.path})
	}
	container["env"] = env
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": cfg.Name, "namespace": cfg.Namespace, "labels": stringMapAny(labels)}, "spec": spec}}, nil
}
