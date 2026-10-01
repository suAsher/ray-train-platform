package config

import (
	"os"
	"strings"
	"testing"
)

func TestStorageSyncDisabledByDefault(t *testing.T) {
	t.Setenv("STORAGE_SYNC_ENABLED", "false")
	cfg, err := loadStorageSyncConfig()
	if err != nil || cfg.Enabled {
		t.Fatalf("disabled configuration: %#v %v", cfg, err)
	}
}

func storageSyncTestEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"STORAGE_SYNC_ENABLED":            "true",
		"STORAGE_SYNC_IMAGE":              "registry.example/storage-sync@sha256:" + strings.Repeat("a", 64),
		"STORAGE_SYNC_NAMESPACE":          "ray-train-platform",
		"STORAGE_SYNC_BUCKET":             "storage-bucket",
		"STORAGE_SYNC_REGION":             "cn-shanghai",
		"STORAGE_SYNC_ENDPOINT":           "https://tos-cn-shanghai.volces.com",
		"STORAGE_SYNC_CREDENTIAL_SECRET":  "storage-sync-config",
		"STORAGE_SYNC_SERVICE_ACCOUNT":    "storage-sync-worker",
		"STORAGE_SYNC_WORK_CLAIM_NAME":    "storage-sync-work",
		"STORAGE_SYNC_NODE_SELECTOR_JSON": `{"raytrain.wellspiking.ai/storage-sync":"true"}`,
		"STORAGE_SYNC_CALLBACK_BASE_URL":  "http://ray-train-backend:8080",
	} {
		t.Setenv(key, value)
	}
}

func TestStorageSyncConfigRequiresExplicitCPUPlacement(t *testing.T) {
	storageSyncTestEnvironment(t)
	t.Setenv("STORAGE_SYNC_NODE_SELECTOR_JSON", "{}")
	if _, err := loadStorageSyncConfig(); err == nil {
		t.Fatal("enabled worker accepted without explicit CPU node selection")
	}
}

func TestStorageSyncConfigValidatesResourceAndConcurrencyLimits(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"STORAGE_SYNC_IMAGE", "registry.example/storage-sync:latest"},
		{"STORAGE_SYNC_CPU_REQUEST", "9"},
		{"STORAGE_SYNC_MEMORY_REQUEST", "9Gi"},
		{"STORAGE_SYNC_EPHEMERAL_STORAGE_LIMIT", "0"},
		{"STORAGE_SYNC_MAX_ACTIVE_RUNS", "0"},
		{"STORAGE_SYNC_MAX_FILE_CONCURRENCY", "129"},
		{"STORAGE_SYNC_MAX_PART_CONCURRENCY", "65"},
		{"STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND", "102399"},
		{"STORAGE_SYNC_NODE_SELECTOR_JSON", `{"invalid key":"cpu"}`},
		{"STORAGE_SYNC_ENDPOINT", "https://name:password@example.org"},
		{"STORAGE_SYNC_CREDENTIAL_MODE", "automatic"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			storageSyncTestEnvironment(t)
			t.Setenv(tc.key, tc.value)
			if _, err := loadStorageSyncConfig(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestStorageSyncBandwidthCeilingAcceptsSDKMinimum(t *testing.T) {
	storageSyncTestEnvironment(t)
	t.Setenv("STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND", "102400")
	cfg, err := loadStorageSyncConfig()
	if err != nil || cfg.MaxBandwidthBytesPerSecond != 102400 {
		t.Fatalf("valid SDK minimum rejected: %#v %v", cfg, err)
	}
}

func TestStorageSyncConfigurationHasConservativeDefaults(t *testing.T) {
	storageSyncTestEnvironment(t)
	cfg, err := loadStorageSyncConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxActiveRuns != 1 || cfg.MaxFileConcurrency != 4 || cfg.MaxPartConcurrency != 2 || cfg.CredentialMode != "static-secret" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.CPURequest == "" || cfg.CPULimit == "" || cfg.MemoryRequest == "" || cfg.MemoryLimit == "" {
		t.Fatal("worker resources must be explicitly bounded")
	}
}

func TestStorageSyncChartAndBuildContract(t *testing.T) {
	for filename, required := range map[string][]string{
		"../../helm/ray-train-platform/templates/storage-sync.yaml":       {"automountServiceAccountToken: false", "kind: Role", "persistentvolumeclaims", "jobs", "pods"},
		"../../helm/ray-train-platform/templates/backend-deployment.yaml": {"STORAGE_SYNC_ENABLED", "STORAGE_SYNC_NODE_SELECTOR_JSON", "STORAGE_SYNC_WORK_CLAIM_NAME", "STORAGE_SYNC_MAX_ACTIVE_RUNS"},
		"../../helm/ray-train-platform/values.yaml":                       {"storageSync:", "credentialMode: static-secret"},
		"../../build-image.sh":                                            {"storage-sync)", "images/storage-sync/Dockerfile|ray-storage-sync|images/storage-sync|-"},
	} {
		content, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range required {
			if !strings.Contains(string(content), expected) {
				t.Errorf("%s missing %q", filename, expected)
			}
		}
	}
}
