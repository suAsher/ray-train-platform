package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestVKECPUHAProfilePinsProductionTrainingPoolToSixteenGPUs(t *testing.T) {
	contents, err := os.ReadFile("../../deploy/profiles/vke-cpu-ha.yaml")
	if err != nil {
		t.Fatalf("read VKE production profile: %v", err)
	}
	profile := string(contents)
	for _, expected := range []string{
		"nodeSelector: accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production",
		"maxWorkerReplicas: 2",
		"maxGPUsPerWorker: 8",
		"maxTotalGPUs: 16",
		"gpuQuota: 16",
		"publicRoot: ray-train/public",
		"platform.wellspiking.ai/gpu-pool: production",
		"prometheusURL: http://prometheus-prometheus.monitoring.svc.cluster.local:9090",
	} {
		if !strings.Contains(profile, expected) {
			t.Fatalf("VKE production profile must contain %q", expected)
		}
	}
	backendImage := regexp.MustCompile(`(?ms)repository: ray-train-backend\s+tag: [^\n]+\s+digest: sha256:[a-f0-9]{64}`)
	if !backendImage.MatchString(profile) {
		t.Fatal("VKE production profile must pin the backend to an immutable SHA256 digest")
	}
	// The workspace and source-materializer images are asserted the same way.
	// Pinning one exact digest here would break on every legitimate image
	// rebuild while testing nothing the regex does not already cover.
	for _, field := range []string{"workspaceImage", "sourceMaterializerImage"} {
		pinned := regexp.MustCompile(field + `: \S+@sha256:[a-f0-9]{64}`)
		if !pinned.MatchString(profile) {
			t.Fatalf("VKE production profile must pin %s to an immutable SHA256 digest", field)
		}
	}
}
