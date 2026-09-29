package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func environmentRegistryConfig(t *testing.T) {
	t.Helper()
	t.Setenv("ENVIRONMENT_BUILDS_ENABLED", "true")
	for _, name := range []string{"ENVIRONMENT_BASE_IMAGE", "ENVIRONMENT_WORKSPACE_IMAGE", "ENVIRONMENT_PREPARE_IMAGE", "ENVIRONMENT_PUBLISHER_IMAGE"} {
		t.Setenv(name, "harbor.wellspiking.ai/public/test@sha256:"+strings.Repeat("a", 64))
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
		{"legacy default", "", []string{"harbor.wellspiking.ai"}},
		{"both destinations", "harbor.wellspiking.ai,harbor.qomolo.com", []string{"harbor.wellspiking.ai", "harbor.qomolo.com"}},
		{"trimmed deduplicated", " harbor.qomolo.com , harbor.wellspiking.ai,harbor.qomolo.com ", []string{"harbor.qomolo.com", "harbor.wellspiking.ai"}},
		{"qomolo only", "harbor.qomolo.com", []string{"harbor.qomolo.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environmentRegistryConfig(t)
			t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", tc.value)
			cfg, err := loadEnvironmentBuildConfig()
			if err != nil || !reflect.DeepEqual(cfg.RegistryHosts, tc.want) {
				t.Fatalf("registry allowlist = %v, error = %v", cfg.RegistryHosts, err)
			}
		})
	}
}

func TestEnvironmentBuildRegistryAllowlistRejectsNonCanonicalHosts(t *testing.T) {
	for _, value := range []string{"evil.example", "https://harbor.qomolo.com", "harbor.qomolo.com:443", "harbor.qomolo.com/", "harbor.qomolo.com.evil.example", "HARBOR.QOMOLO.COM", ",harbor.qomolo.com", "harbor.qomolo.com,", "harbor.qomolo.com,,harbor.wellspiking.ai"} {
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
			t.Setenv("ENVIRONMENT_REGISTRY_HOSTS", "harbor.wellspiking.ai,harbor.qomolo.com")
			t.Setenv(name, "harbor.qomolo.com/public/test@sha256:"+strings.Repeat("a", 64))
			if _, err := loadEnvironmentBuildConfig(); err == nil {
				t.Fatal("destination allowlist changed the source image trust boundary")
			}
		})
	}
}

func TestEnvironmentBuildRegistryChartDefaults(t *testing.T) {
	values, err := os.ReadFile("../../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "registryHosts:\n      - harbor.wellspiking.ai") {
		t.Fatal("chart must keep the legacy single-registry default")
	}
	if !strings.Contains(string(deployment), "name: ENVIRONMENT_REGISTRY_HOSTS") || !strings.Contains(string(deployment), `join "," ($environmentBuilds.registryHosts | default (list "harbor.wellspiking.ai"))`) {
		t.Fatal("chart must render the destination allowlist with an old-values fallback")
	}
}
