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
	deployment, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatalf("read backend deployment template: %v", err)
	}
	if !strings.Contains(string(deployment), "name: KUEUE_TOPOLOGY_ENABLED\n              value: {{ .Values.kueue.topology.enabled | quote }}") {
		t.Fatal("backend worker annotations and ResourceFlavor topology must use the same Helm cutover switch")
	}
	if !strings.Contains(string(deployment), "name: KUEUE_PREEMPTION_ENABLED\n              value: {{ default false (get $kueuePreemption \"enabled\") | quote }}") {
		t.Fatal("backend submission gate and ClusterQueue preemption must use the same Helm switch")
	}
	if !strings.Contains(string(deployment), "$kueuePreemption := default (dict) .Values.kueue.preemption") {
		t.Fatal("backend deployment must default a missing preemption map for --reuse-values upgrades")
	}
}

func TestChartKeepsOpportunisticPreemptionBehindExplicitCutover(t *testing.T) {
	values, err := os.ReadFile("../../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "preemption:\n    enabled: false") {
		t.Fatal("preemption must be disabled by default")
	}
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/kueue-resources.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contents := string(template)
	for _, required := range []string{"withinClusterQueue:", "LowerPriority", "$preemptionEnabled", "default (dict) .Values.kueue.preemption"} {
		if !strings.Contains(contents, required) {
			t.Fatalf("preemption template is missing %q", required)
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
	if !strings.Contains(string(values), "resourceFlavorName: gpu-4090-tas-flavor") {
		t.Fatal("TAS cutover must create a new flavor because ResourceFlavor.spec is immutable")
	}
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/kueue-resources.yaml")
	if err != nil {
		t.Fatalf("read Kueue resources template: %v", err)
	}
	contents := string(template)
	for _, required := range []string{"kind: Topology", "topologyName:", ".Values.kueue.topology.enabled", ".Values.kueue.topology.nodeLabel", "$resourceFlavorName :=", ".Values.kueue.acceleratorFlavors", "$flavor.tasName"} {
		if !strings.Contains(contents, required) {
			t.Fatalf("TAS template is missing %q", required)
		}
	}
}

func TestChartDefinesOneFlavorPerSupportedAccelerator(t *testing.T) {
	values, err := os.ReadFile("../../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contents := string(values)
	if !strings.Contains(contents, "multiFlavor:\n    enabled: false") {
		t.Fatal("multi-flavor scheduling must remain behind an explicit idle-cluster cutover")
	}
	for _, required := range []string{
		"rtx4090:", "gpu-4090-flavor", "accelerator: nvidia-rtx-4090",
		"a100:", "gpu-a100-flavor", "accelerator: nvidia-a100",
		"a800:", "gpu-a800-flavor", "accelerator: nvidia-a800",
		"h20:", "gpu-h20-flavor", "accelerator: nvidia-h20",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("accelerator flavor values are missing %q", required)
		}
	}
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/kueue-resources.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(template), "default (dict) .Values.kueue.multiFlavor") ||
		!strings.Contains(string(template), "$multiFlavorEnabled") ||
		!strings.Contains(string(template), ".Values.kueue.resourceFlavorName") {
		t.Fatal("chart must preserve the legacy single flavor until multi-flavor cutover")
	}
}
