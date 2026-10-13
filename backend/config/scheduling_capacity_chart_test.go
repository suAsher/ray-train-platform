package config

import (
	"os"
	"strings"
	"testing"
)

func TestChartGrantsOnlyGetOnCapacityResourceFlavors(t *testing.T) {
	contents, err := os.ReadFile("../../helm/ray-train-platform/templates/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "resources: [\"resourceflavors\"]\n    verbs: [\"get\"]") {
		t.Fatal("capacity pool isolation requires narrowly scoped resourceflavors get permission")
	}
}
