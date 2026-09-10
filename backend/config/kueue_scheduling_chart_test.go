package config

import (
	"os"
	"strings"
	"testing"
)

func TestChartDefinesKueueWorkloadPriorityClasses(t *testing.T) {
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/training-priorityclasses.yaml")
	if err != nil {
		t.Fatalf("read workload priority template: %v", err)
	}
	contents := string(template)
	for _, required := range []string{
		"apiVersion: kueue.x-k8s.io/v1beta2",
		"kind: WorkloadPriorityClass",
		"raytrain-production",
		"raytrain-normal",
		"raytrain-opportunistic",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("workload priority template is missing %q", required)
		}
	}
	for _, forbidden := range []string{"kind: PriorityClass", "globalDefault:", "preemptionPolicy:"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("Kueue workload priority template must not contain Kubernetes PriorityClass field %q", forbidden)
		}
	}
}

func TestChartKeepsTopologyAwareSchedulingBehindExplicitCutover(t *testing.T) {
	values, err := os.ReadFile("../../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatalf("read chart values: %v", err)
	}
	if !strings.Contains(string(values), "topology:\n    enabled: false") {
		t.Fatal("TAS must remain disabled until the legacy queue has no admitted workloads")
	}
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/kueue-resources.yaml")
	if err != nil {
		t.Fatalf("read Kueue resources template: %v", err)
	}
	contents := string(template)
	for _, required := range []string{"kind: Topology", "topologyName:", ".Values.kueue.topology.enabled", ".Values.kueue.topology.nodeLabel"} {
		if !strings.Contains(contents, required) {
			t.Fatalf("TAS template is missing %q", required)
		}
	}
}
