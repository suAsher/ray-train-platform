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
	for _, want := range []string{"$assistant.idleNamespace", "kind: Role\n", "kind: RoleBinding", `resources: ["configmaps"]`, `resources: ["rayservices"]`, `verbs: ["get"]`, "name: ray-train-platform-sa", "namespace: {{ .Release.Namespace }}"} {
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
