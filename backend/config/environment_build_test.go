package config

import "testing"

func TestEnvironmentBuildDisabledByDefault(t *testing.T) {
	t.Setenv("ENVIRONMENT_BUILDS_ENABLED", "false")
	cfg, err := loadEnvironmentBuildConfig()
	if err != nil || cfg.Enabled { t.Fatalf("disabled configuration: %v", err) }
}

func TestEnvironmentBuildRequiresPinnedImagesAndSeparateKey(t *testing.T) {
	t.Setenv("ENVIRONMENT_BUILDS_ENABLED", "true")
	if _, err := loadEnvironmentBuildConfig(); err == nil { t.Fatal("enabled without pinned runtime images or key") }
	for _, name := range []string{"ENVIRONMENT_BASE_IMAGE", "ENVIRONMENT_WORKSPACE_IMAGE", "ENVIRONMENT_PREPARE_IMAGE", "ENVIRONMENT_PUBLISHER_IMAGE"} {
		t.Setenv(name, "harbor.wellspiking.ai/public/test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	t.Setenv("ENVIRONMENT_AUTH_KEY", "invalid")
	if _, err := loadEnvironmentBuildConfig(); err == nil { t.Fatal("invalid encryption key accepted") }
	t.Setenv("ENVIRONMENT_AUTH_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("ENVIRONMENT_WHEEL_INDEX_URL", "https://packages.internal/simple")
	if _, err := loadEnvironmentBuildConfig(); err != nil { t.Fatalf("valid configuration: %v", err) }
	t.Setenv("ENVIRONMENT_BASE_IMAGE", "harbor.wellspiking.ai/public/test:latest")
	if _, err := loadEnvironmentBuildConfig(); err == nil { t.Fatal("mutable base accepted") }
}
