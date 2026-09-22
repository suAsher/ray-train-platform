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

type labelSelector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

func TestAssistantIdleKubeRayDashboardPolicyPinsOperatorPods(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "assistant-idle")
	networkPolicy, err := os.ReadFile(filepath.Join(root, "networkpolicy.yaml"))
	if err != nil {
		t.Fatalf("read networkpolicy.yaml: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	policy := networkPolicyNamed(t, string(networkPolicy), "assistant-idle-kuberay-dashboard-only")
	if !hasPinnedKubeRayOperatorPeer(policy) {
		t.Fatalf("KubeRay dashboard policy must restrict one peer by both namespace and operator pod labels: %#v", policy.Spec.Ingress)
	}

	mustContain(t, string(readme), "PLACEHOLDER_KUBERAY_OPERATOR_NAMESPACE")
	mustContain(t, string(readme), "app.kubernetes.io/name=kuberay-operator")
	mustContain(t, string(readme), "app.kubernetes.io/component=kuberay-operator")
	mustContain(t, string(readme), "app.kubernetes.io/instance=kuberay")
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

func hasPinnedKubeRayOperatorPeer(policy networkPolicyContract) bool {
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 {
		return false
	}
	for _, ingress := range policy.Spec.Ingress {
		for _, peer := range ingress.From {
			if peer.NamespaceSelector == nil || peer.PodSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "PLACEHOLDER_KUBERAY_OPERATOR_NAMESPACE" {
				continue
			}
			labels := peer.PodSelector.MatchLabels
			if labels["app.kubernetes.io/name"] == "kuberay-operator" && labels["app.kubernetes.io/component"] == "kuberay-operator" && labels["app.kubernetes.io/instance"] == "kuberay" {
				return true
			}
		}
	}
	return false
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
