package assistantidle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	policy := yamlDocumentNamed(t, string(networkPolicy), "assistant-idle-kuberay-dashboard-only")
	mustContain(t, policy, "namespaceSelector:")
	mustContain(t, policy, "kubernetes.io/metadata.name: PLACEHOLDER_KUBERAY_OPERATOR_NAMESPACE")
	mustContain(t, policy, "podSelector:")
	mustContain(t, policy, "app.kubernetes.io/name: kuberay-operator")
	mustContain(t, policy, "app.kubernetes.io/component: kuberay-operator")
	mustContain(t, policy, "app.kubernetes.io/instance: kuberay")

	mustContain(t, string(readme), "PLACEHOLDER_KUBERAY_OPERATOR_NAMESPACE")
	mustContain(t, string(readme), "app.kubernetes.io/name=kuberay-operator")
	mustContain(t, string(readme), "app.kubernetes.io/component=kuberay-operator")
	mustContain(t, string(readme), "app.kubernetes.io/instance=kuberay")
}

func yamlDocumentNamed(t *testing.T, body, name string) string {
	t.Helper()
	for _, document := range strings.Split(body, "---") {
		if strings.Contains(document, "name: "+name) {
			return document
		}
	}
	t.Fatalf("missing YAML document named %s in:\n%s", name, body)
	return ""
}
