package assistantidle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type networkPolicyContract struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Ingress []struct {
			From []networkPolicyPeer `yaml:"from"`
		} `yaml:"ingress"`
	} `yaml:"spec"`
}

type networkPolicyPeer struct {
	NamespaceSelector *labelSelector `yaml:"namespaceSelector"`
	PodSelector       *labelSelector `yaml:"podSelector"`
}

type serviceContract struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Type     string            `yaml:"type"`
		Selector map[string]string `yaml:"selector"`
		Ports    []struct {
			Name       string `yaml:"name"`
			Port       int    `yaml:"port"`
			TargetPort any    `yaml:"targetPort"`
			NodePort   int    `yaml:"nodePort"`
		} `yaml:"ports"`
	} `yaml:"spec"`
}

type labelSelector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

func TestAssistantIdleNetworkDoesNotExposeRayControlPorts(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "assistant-idle", "networkpolicy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"kuberay-dashboard", "port: 8265", "port: 6379", "port: 10001", "port: 8000"} {
		mustNotContain(t, string(body), forbidden)
	}
	policy := networkPolicyNamed(t, string(body), "assistant-idle-backend-to-serve-only")
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 {
		t.Fatal("inference ingress must have one pinned backend peer")
	}
	peer := policy.Spec.Ingress[0].From[0]
	if peer.NamespaceSelector == nil || peer.PodSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "PLACEHOLDER_RAYTRAIN_BACKEND_NAMESPACE" || peer.PodSelector.MatchLabels["app"] != "ray-train-backend" || peer.PodSelector.MatchLabels["app.kubernetes.io/component"] != "api" {
		t.Fatal("inference backend peer widened")
	}

}

func TestAssistantIdleInferenceServiceRoutesOnlyTLSInferencePort(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "assistant-idle")
	serviceBody, err := os.ReadFile(filepath.Join(root, "service.yaml"))
	if err != nil {
		t.Fatalf("read service.yaml: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	service := serviceNamed(t, string(serviceBody), "assistant-idle-inference")
	if service.Spec.Type != "ClusterIP" {
		t.Fatalf("inference service must be ClusterIP, got %q", service.Spec.Type)
	}
	if len(service.Spec.Ports) != 1 {
		t.Fatalf("inference service must expose only one port, got %#v", service.Spec.Ports)
	}
	port := service.Spec.Ports[0]
	if port.Port != 8443 || intValue(port.TargetPort) != 8443 || port.NodePort != 0 {
		t.Fatalf("inference service must route only ClusterIP port 8443 to inference port 8443 without NodePort: %#v", port)
	}
	for key, want := range map[string]string{
		"app.kubernetes.io/instance":             "PLACEHOLDER_INSTANCE_ID",
		"app.kubernetes.io/component":            "assistant-idle",
		"raytrain.wellspiking.ai/assistant-role": "inference",
	} {
		if service.Spec.Selector[key] != want {
			t.Fatalf("inference service selector[%s]=%q, want %q in %#v", key, service.Spec.Selector[key], want, service.Spec.Selector)
		}
	}
	for _, forbidden := range []string{"ray.io/serve", "ray.io/cluster"} {
		if _, found := service.Spec.Selector[forbidden]; found {
			t.Fatalf("inference service must not depend on KubeRay serve selector %s: %#v", forbidden, service.Spec.Selector)
		}
	}

	readmeBody := string(readme)
	mustContain(t, readmeBody, "assistant-idle-inference")
	mustContain(t, readmeBody, "runtimeType: \"pod\"")
	mustContain(t, readmeBody, "TLS")
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func serviceNamed(t *testing.T, body, name string) serviceContract {
	t.Helper()
	for _, document := range strings.Split(body, "---") {
		document = strings.TrimSpace(document)
		if document == "" {
			continue
		}
		var service serviceContract
		if err := yaml.Unmarshal([]byte(document), &service); err != nil {
			t.Fatalf("parse Service document: %v\n%s", err, document)
		}
		if service.Kind == "Service" && service.Metadata.Name == name {
			return service
		}
	}
	t.Fatalf("missing Service named %s in:\n%s", name, body)
	return serviceContract{}
}

func networkPolicyNamed(t *testing.T, body, name string) networkPolicyContract {
	t.Helper()
	for _, document := range strings.Split(body, "---") {
		document = strings.TrimSpace(document)
		if document == "" {
			continue
		}
		var policy networkPolicyContract
		if err := yaml.Unmarshal([]byte(document), &policy); err != nil {
			t.Fatalf("parse NetworkPolicy document: %v\n%s", err, document)
		}
		if policy.Kind == "NetworkPolicy" && policy.Metadata.Name == name {
			return policy
		}
	}
	t.Fatalf("missing NetworkPolicy named %s in:\n%s", name, body)
	return networkPolicyContract{}
}

func TestAssistantIdleDeployReferencesPreparedImagePullSecretsAndModelPVC(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "assistant-idle")
	files := map[string]string{}
	for _, name := range []string{"config.example.json", "deployment-controller.yaml", "deployment-reaper.yaml", "README.md"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = string(body)
	}

	mustContain(t, files["config.example.json"], `"ImagePullSecrets": ["harbor-registry"]`)
	for _, name := range []string{"deployment-controller.yaml", "deployment-reaper.yaml"} {
		mustContain(t, files[name], "imagePullSecrets:")
		mustContain(t, files[name], "name: PLACEHOLDER_IMAGE_PULL_SECRET_NAME")
	}

	readme := files["README.md"]
	mustContain(t, readme, "harbor-registry")
	mustContain(t, readme, "ImagePullSecrets")
	mustContain(t, readme, "imagePullSecrets")
	mustContain(t, readme, "ModelPVC")
	mustContain(t, readme, "ebs-ssd")
	mustContain(t, readme, "RWO")
	mustContain(t, readme, "node affinity")
}
