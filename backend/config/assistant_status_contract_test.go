package config

import (
	"os"
	"strings"
	"testing"
)

func TestAssistantStatusNamespaceScopedRBACContract(t *testing.T) {
	raw, err := os.ReadFile("../../helm/ray-train-platform/templates/assistant-status-rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"$assistant.idleNamespace", "kind: Role\n", "kind: RoleBinding", `resources: ["configmaps"]`, `resources: ["rayservices"]`, `verbs: ["get"]`, "name: ray-train-platform-sa", "namespace: {{ .Values.namespace }}"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing scope contract %q", want)
		}
	}
	for _, bad := range []string{"ClusterRole", `"list"`, `"secrets"`, `"create"`, `"update"`, `"delete"`, `"patch"`} {
		if strings.Contains(s, bad) {
			t.Fatalf("overbroad status permission %q", bad)
		}
	}
}
func TestAssistantStatusControllerNetworkPolicyContract(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/assistant-idle/networkpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	start := strings.Index(s, "name: assistant-idle-controller-gate")
	if start < 0 {
		t.Fatal("gate policy absent")
	}
	s = s[start:]
	if end := strings.Index(s, "\n---"); end >= 0 {
		s = s[:end]
	}
	for _, want := range []string{"namespaceSelector:", "kubernetes.io/metadata.name: PLACEHOLDER_RAYTRAIN_BACKEND_NAMESPACE", "app: ray-train-backend", "app.kubernetes.io/component: api", "port: 8443"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing scoped status path %q", want)
		}
	}
	if strings.Contains(s, "port: 8080") { t.Fatal("controller must not expose the old cleartext gate") }
}
