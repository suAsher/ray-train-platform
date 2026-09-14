package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
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
	if !strings.Contains(string(deployment), "name: KUEUE_TOPOLOGY_ENABLED\n              value: {{ $kueueWorkloadTopologyEnabled | quote }}") {
		t.Fatal("backend worker annotations must use the independently controlled topology request switch")
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
		t.Fatal("TAS must remain disabled by default until an explicit reviewed rollout")
	}
	var decoded struct {
		Kueue struct {
			ResourceFlavorName string `yaml:"resourceFlavorName"`
			Topology           struct {
				ResourceFlavorName string `yaml:"resourceFlavorName"`
				WorkloadEnabled    *bool  `yaml:"workloadEnabled"`
			} `yaml:"topology"`
		} `yaml:"kueue"`
	}
	if err := yaml.Unmarshal(values, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kueue.Topology.WorkloadEnabled != nil {
		t.Fatal("default workloadEnabled must be absent to inherit topology.enabled")
	}
	if decoded.Kueue.ResourceFlavorName != "gpu-4090-flavor" || decoded.Kueue.Topology.ResourceFlavorName != decoded.Kueue.ResourceFlavorName {
		t.Fatal("initial single-flavor TAS rollout must preserve the existing flavor name and quota")
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

func TestChartCanDisableNewTopologyRequestsWithoutRemovingFlavorTopology(t *testing.T) {
	deployment, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Execute the actual switch block; full-chart Helm rendering is a release gate.
	contents := string(deployment)
	start := strings.Index(contents, "{{- $kueueTopology :=")
	end := strings.Index(contents, "{{- if (default false $datasetPublisher.enabled)")
	if start < 0 || end <= start {
		t.Fatal("cannot locate topology switch block")
	}
	funcs := template.FuncMap{
		"dict": func() map[string]any { return map[string]any{} },
		"default": func(fallback, value any) any {
			if value == nil || value == false {
				return fallback
			}
			return value
		},
		"get":    func(values map[string]any, key string) any { return values[key] },
		"hasKey": func(values map[string]any, key string) bool { _, ok := values[key]; return ok },
		"kindIs": func(kind string, value any) bool { _, ok := value.(bool); return kind == "bool" && ok },
		"fail":   func(message string) (string, error) { return "", fmt.Errorf("%s", message) },
	}
	tmpl, err := template.New("topology").Funcs(funcs).Parse(contents[start:end] + "{{ $kueueWorkloadTopologyEnabled }}")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name      string
		topology  map[string]any
		want      string
		wantError bool
	}{
		{"missing map", nil, "false", false},
		{"default disabled", map[string]any{"enabled": false}, "false", false},
		{"inherit enabled", map[string]any{"enabled": true}, "true", false},
		{"explicit enabled", map[string]any{"enabled": true, "workloadEnabled": true}, "true", false},
		{"retain resources only", map[string]any{"enabled": true, "workloadEnabled": false}, "false", false},
		{"both disabled", map[string]any{"enabled": false, "workloadEnabled": false}, "false", false},
		{"missing topology resources", map[string]any{"enabled": false, "workloadEnabled": true}, "", true},
		{"reject string false", map[string]any{"enabled": true, "workloadEnabled": "false"}, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := tmpl.Execute(&output, map[string]any{"Values": map[string]any{"kueue": map[string]any{"topology": tt.topology}}})
			if (err != nil) != tt.wantError || (!tt.wantError && strings.TrimSpace(output.String()) != tt.want) {
				t.Fatalf("topology result = %q, %v; want %q, error=%v", output.String(), err, tt.want, tt.wantError)
			}
		})
	}
	resources, err := os.ReadFile("../../helm/ray-train-platform/templates/kueue-resources.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resources), "workloadEnabled") {
		t.Fatal("disabling new topology requests must not remove the Topology or flavor topologyName")
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
