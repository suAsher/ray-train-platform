package config

import (
	"os"
	"strings"
	"testing"
)

func TestChartGrantsOnlyCreateOnPodExecForWorkerConnection(t *testing.T) {
	contents, err := os.ReadFile("../../helm/ray-train-platform/templates/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rbac := string(contents)
	if !strings.Contains(rbac, "resources: [\"pods/exec\"]\n    verbs: [\"create\"]") {
		t.Fatal("worker connection requires the narrowly scoped pods/exec create permission")
	}
}
