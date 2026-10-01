package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentBuildCompatibleWorkspaceImagesNormalizeExplicitAllowlist(t *testing.T) {
	primary := "harbor.wellspiking.ai/public/test@sha256:" + strings.Repeat("a", 64)
	legacy := "harbor.wellspiking.ai/public/workspace@sha256:" + strings.Repeat("b", 64)
	older := "harbor.wellspiking.ai/public/workspace@sha256:" + strings.Repeat("c", 64)
	for _, test := range []struct {
		name, raw string
		want []string
	}{
		{name: "empty defaults", raw: ""},
		{name: "explicit old image", raw: legacy, want: []string{legacy}},
		{name: "trim deduplicate and omit primary", raw: " " + legacy + ", " + primary + "," + legacy + ", " + older + " ", want: []string{legacy, older}},
	} {
		t.Run(test.name, func(t *testing.T) {
			environmentRegistryConfig(t)
			t.Setenv("ENVIRONMENT_COMPATIBLE_WORKSPACE_IMAGES", test.raw)
			cfg, err := loadEnvironmentBuildConfig()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var got struct { CompatibleWorkspaceImages []string }
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.CompatibleWorkspaceImages, test.want) || cfg.WorkspaceImage != primary {
				t.Fatalf("compatible images=%v want=%v, primary=%s", got.CompatibleWorkspaceImages, test.want, cfg.WorkspaceImage)
			}
		})
	}
}

func TestEnvironmentBuildCompatibleWorkspaceImagesRejectUntrustedEntries(t *testing.T) {
	valid := "harbor.wellspiking.ai/public/workspace@sha256:" + strings.Repeat("b", 64)
	for _, raw := range []string{
		"harbor.wellspiking.ai/public/workspace:latest",
		"harbor.wellspiking.ai/public/workspace@sha256:short",
		"harbor.qomolo.com/public/workspace@sha256:" + strings.Repeat("b", 64),
		"harbor.wellspiking.ai.evil.test/public/workspace@sha256:" + strings.Repeat("b", 64),
		"https://" + valid, valid + ",", "," + valid, valid + ",," + valid,
	} {
		t.Run(raw, func(t *testing.T) {
			environmentRegistryConfig(t)
			t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", "harbor.wellspiking.ai,harbor.qomolo.com")
			t.Setenv("ENVIRONMENT_COMPATIBLE_WORKSPACE_IMAGES", raw)
			if _, err := loadEnvironmentBuildConfig(); err == nil || !strings.Contains(err.Error(), "ENVIRONMENT_COMPATIBLE_WORKSPACE_IMAGES") {
				t.Fatalf("invalid compatible image allowlist accepted or misreported: %q (%v)", raw, err)
			}
		})
	}
}
