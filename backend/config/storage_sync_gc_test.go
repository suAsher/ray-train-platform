package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestStorageSyncGCConfigurationDefaults(t *testing.T) {
	storageSyncTestEnvironment(t)
	cfg, err := loadStorageSyncConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.GCNamespaces, []string{"ray-train-platform"}) {
		t.Fatalf("execution namespace must be included in GC: %#v", cfg.GCNamespaces)
	}
	if cfg.GCSucceededTTLSeconds != 3600 || cfg.GCFailedTTLSeconds != 86400 {
		t.Fatalf("unexpected GC retention: %d / %d", cfg.GCSucceededTTLSeconds, cfg.GCFailedTTLSeconds)
	}
}

func TestStorageSyncGCConfigurationIncludesOnlyOperatorNamespaces(t *testing.T) {
	storageSyncTestEnvironment(t)
	t.Setenv("STORAGE_SYNC_NAMESPACE", "ray-train-sync")
	t.Setenv("STORAGE_SYNC_GC_NAMESPACES_JSON", `["ray-train-platform","ray-train-sync","ray-train-platform"]`)
	t.Setenv("STORAGE_SYNC_GC_SUCCEEDED_TTL_SECONDS", "7200")
	t.Setenv("STORAGE_SYNC_GC_FAILED_TTL_SECONDS", "172800")
	cfg, err := loadStorageSyncConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.GCNamespaces, []string{"ray-train-sync", "ray-train-platform"}) {
		t.Fatalf("allowlist was not normalized: %#v", cfg.GCNamespaces)
	}
	if cfg.GCSucceededTTLSeconds != 7200 || cfg.GCFailedTTLSeconds != 172800 {
		t.Fatalf("configured retention ignored: %#v", cfg)
	}
}

func TestStorageSyncGCConfigurationRejectsUnsafeValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"STORAGE_SYNC_GC_NAMESPACES_JSON", `"ray-train-platform"`},
		{"STORAGE_SYNC_GC_NAMESPACES_JSON", `null`},
		{"STORAGE_SYNC_GC_NAMESPACES_JSON", `["*"]`},
		{"STORAGE_SYNC_GC_NAMESPACES_JSON", `[""]`},
		{"STORAGE_SYNC_GC_NAMESPACES_JSON", `["team.prod"]`},
		{"STORAGE_SYNC_GC_NAMESPACES_JSON", `["` + strings.Repeat("a", 64) + `"]`},
		{"STORAGE_SYNC_NAMESPACE", "team.prod"},
		{"STORAGE_SYNC_GC_SUCCEEDED_TTL_SECONDS", "0"},
		{"STORAGE_SYNC_GC_SUCCEEDED_TTL_SECONDS", "-1"},
		{"STORAGE_SYNC_GC_FAILED_TTL_SECONDS", "0"},
		{"STORAGE_SYNC_GC_FAILED_TTL_SECONDS", "604801"},
		{"STORAGE_SYNC_GC_FAILED_TTL_SECONDS", "8.64e4"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			storageSyncTestEnvironment(t)
			t.Setenv(tc.key, tc.value)
			if _, err := loadStorageSyncConfig(); err == nil {
				t.Fatal("unsafe GC configuration accepted")
			}
		})
	}
}
