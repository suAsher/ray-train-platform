package config

import (
	"reflect"
	"strings"
	"testing"
)

func environmentRegistryConfig(t *testing.T) {
	t.Helper()
	t.Setenv("ENVIRONMENT_BUILDS_ENABLED", "true")
	t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", "registry.example.com")
	for _, name := range []string{"ENVIRONMENT_BASE_IMAGE", "ENVIRONMENT_WORKSPACE_IMAGE", "ENVIRONMENT_PREPARE_IMAGE", "ENVIRONMENT_PUBLISHER_IMAGE"} {
		t.Setenv(name, "registry.example.com/public/test@sha256:"+strings.Repeat("a", 64))
	}
	t.Setenv("ENVIRONMENT_AUTH_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("ENVIRONMENT_WHEEL_INDEX_URL", "https://packages.internal/simple")
}

func TestEnvironmentBuildRegistryAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  []string
	}{
		{"custom host", "harbor.custom.example", []string{"harbor.custom.example"}},
		{"both destinations", "registry.example.com,registry-secondary.example.com", []string{"registry.example.com", "registry-secondary.example.com"}},
		{"trimmed deduplicated", " registry-secondary.example.com , registry.example.com,registry-secondary.example.com ", []string{"registry-secondary.example.com", "registry.example.com"}},
		{"secondary only", "registry-secondary.example.com", []string{"registry-secondary.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environmentRegistryConfig(t)
			t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", tc.value)
			for _, name := range []string{"ENVIRONMENT_BASE_IMAGE", "ENVIRONMENT_WORKSPACE_IMAGE", "ENVIRONMENT_PREPARE_IMAGE", "ENVIRONMENT_PUBLISHER_IMAGE"} {
				t.Setenv(name, tc.want[0]+"/project/test@sha256:"+strings.Repeat("a", 64))
			}
			cfg, err := loadEnvironmentBuildConfig()
			if err != nil || !reflect.DeepEqual(cfg.RegistryHosts, tc.want) {
				t.Fatalf("registry allowlist = %v, error = %v", cfg.RegistryHosts, err)
			}
		})
	}
}

func TestEnvironmentBuildRegistryAllowlistRejectsNonCanonicalHosts(t *testing.T) {
	for _, value := range []string{"", "https://registry-secondary.example.com", "registry-secondary.example.com:443", "registry-secondary.example.com/", "REGISTRY-SECONDARY.EXAMPLE.COM", ",registry-secondary.example.com", "registry-secondary.example.com,", "registry-secondary.example.com,,registry.example.com"} {
		t.Run(value, func(t *testing.T) {
			environmentRegistryConfig(t)
			t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", value)
			if _, err := loadEnvironmentBuildConfig(); err == nil {
				t.Fatal("invalid registry allowlist accepted")
			}
		})
	}
}

func TestEnvironmentBuildRegistryDoesNotBroadenSourceImages(t *testing.T) {
	for _, name := range []string{"ENVIRONMENT_BASE_IMAGE", "ENVIRONMENT_WORKSPACE_IMAGE", "ENVIRONMENT_PREPARE_IMAGE", "ENVIRONMENT_PUBLISHER_IMAGE"} {
		t.Run(name, func(t *testing.T) {
			environmentRegistryConfig(t)
			t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", "registry.example.com")
			t.Setenv(name, "registry-secondary.example.com/public/test@sha256:"+strings.Repeat("a", 64))
			if _, err := loadEnvironmentBuildConfig(); err == nil {
				t.Fatal("destination allowlist changed the source image trust boundary")
			}
		})
	}
}
