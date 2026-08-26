package config

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type vkeCPUHAProfile struct {
	Placement struct {
		PreferredNodeSelector map[string]string `yaml:"preferredNodeSelector"`
		AllowGPUNodeFallback  bool              `yaml:"allowGPUNodeFallback"`
	} `yaml:"placement"`
	Backend struct {
		NodeSelector map[string]string `yaml:"nodeSelector"`
	} `yaml:"backend"`
	Frontend struct {
		NodeSelector map[string]string `yaml:"nodeSelector"`
	} `yaml:"frontend"`
	SpkRayjobRelease struct {
		NodeSelector map[string]string `yaml:"nodeSelector"`
	} `yaml:"spkRayjobRelease"`
	Training struct {
		NodeSelector string `yaml:"nodeSelector"`
		LocalCache   struct {
			Available         bool   `yaml:"available"`
			StorageClassData1 string `yaml:"storageClassData1"`
			StorageClassData2 string `yaml:"storageClassData2"`
			MountPathData1    string `yaml:"mountPathData1"`
			MountPathData2    string `yaml:"mountPathData2"`
			Policy            struct {
				DefaultMode  string   `yaml:"defaultMode"`
				AllowedSizes []string `yaml:"allowedSizes"`
				DefaultSize  string   `yaml:"defaultSize"`
				MaxSize      string   `yaml:"maxSize"`
			} `yaml:"policy"`
		} `yaml:"localCache"`
	} `yaml:"training"`
	Postgres struct {
		Standalone struct {
			NodeSelector map[string]string `yaml:"nodeSelector"`
		} `yaml:"standalone"`
	} `yaml:"postgres"`
}

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

func TestVKECPUHAProfilePrefersCPUButAllowsPhysicalGPUFallback(t *testing.T) {
	contents, err := os.ReadFile("../../deploy/profiles/vke-cpu-ha.yaml")
	if err != nil {
		t.Fatalf("read VKE production profile: %v", err)
	}

	var profile vkeCPUHAProfile
	if err := yaml.Unmarshal(contents, &profile); err != nil {
		t.Fatalf("parse VKE production profile: %v", err)
	}

	if got := profile.Placement.PreferredNodeSelector["platform.wellspiking.ai/pool"]; got != "control-plane" {
		t.Fatalf("preferred control-plane pool=%q", got)
	}
	if !profile.Placement.AllowGPUNodeFallback {
		t.Fatal("VKE production profile must allow physical GPU node fallback")
	}
	for name, selector := range map[string]map[string]string{
		"backend":    profile.Backend.NodeSelector,
		"frontend":   profile.Frontend.NodeSelector,
		"spk-rayjob": profile.SpkRayjobRelease.NodeSelector,
		"postgres":   profile.Postgres.Standalone.NodeSelector,
	} {
		if len(selector) != 0 {
			t.Fatalf("%s must not retain a hard production nodeSelector: %v", name, selector)
		}
	}
	if profile.Training.NodeSelector != "accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production" {
		t.Fatalf("training hard GPU selector changed: %q", profile.Training.NodeSelector)
	}
}

func TestVKECPUHAProfilePublishesCacheAvailabilityWithDefaultOffPolicy(t *testing.T) {
	contents, err := os.ReadFile("../../deploy/profiles/vke-cpu-ha.yaml")
	if err != nil {
		t.Fatalf("read VKE production profile: %v", err)
	}

	var profile vkeCPUHAProfile
	if err := yaml.Unmarshal(contents, &profile); err != nil {
		t.Fatalf("parse VKE production profile: %v", err)
	}
	cache := profile.Training.LocalCache
	if !cache.Available || cache.StorageClassData1 != "ray-cache-local-data1" || cache.StorageClassData2 != "ray-cache-local-data2" || cache.MountPathData1 != "/mnt/cache" || cache.MountPathData2 != "/mnt/cache2" {
		t.Fatalf("unexpected VKE local cache availability: %+v", cache)
	}
	if cache.Policy.DefaultMode != "off" {
		t.Fatalf("per-task cache default mode=%q, want off", cache.Policy.DefaultMode)
	}
	if got := strings.Join(cache.Policy.AllowedSizes, ","); got != "200Gi,500Gi,1Ti,2Ti,4Ti,5Ti" {
		t.Fatalf("cache allowlist=%q", got)
	}
	if cache.Policy.DefaultSize != "200Gi" || cache.Policy.MaxSize != "5Ti" {
		t.Fatalf("cache policy default=%q max=%q", cache.Policy.DefaultSize, cache.Policy.MaxSize)
	}
}

func TestPlatformChartWiresExistingLocalCacheEnvironmentContract(t *testing.T) {
	contents, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatalf("read backend deployment template: %v", err)
	}
	template := string(contents)
	for _, name := range []string{
		"LOCAL_CACHE_ENABLED",
		"LOCAL_CACHE_STORAGE_CLASS_DATA1",
		"LOCAL_CACHE_STORAGE_CLASS_DATA2",
		"LOCAL_CACHE_MOUNT_PATH_DATA1",
		"LOCAL_CACHE_MOUNT_PATH_DATA2",
		"LOCAL_CACHE_SIZE",
		"LOCAL_CACHE_ALLOWED_SIZES",
		"LOCAL_CACHE_MAX_SIZE",
	} {
		if !strings.Contains(template, "name: "+name) {
			t.Fatalf("backend deployment must wire %s", name)
		}
	}
}
