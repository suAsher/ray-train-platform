package config

import (
	"strings"
	"testing"
)

func TestSourceArtifactsDefaultDisabledInDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceArtifactsEnabled {
		t.Fatal("source artifacts must default disabled in development")
	}
}

func TestSourceArtifactsDefaultEnabledAndFailClosedInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "")
	t.Setenv("TOS_ENDPOINT", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "TOS_ENDPOINT") {
		t.Fatalf("production source artifacts must fail closed without TOS endpoint: %v", err)
	}
}

func TestEnabledSourceArtifactsRequireEveryTOSSetting(t *testing.T) {
	settings := map[string]string{
		"TOS_ENDPOINT": "https://tos-cn-beijing.volces.com",
		"TOS_REGION":   "cn-beijing", "TOS_BUCKET": "private-bucket",
		"TOS_ACCESS_KEY": "test-ak", "TOS_SECRET_KEY": "test-sk",
	}
	for missing := range settings {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("SOURCE_ARTIFACTS_ENABLED", "true")
			for key, value := range settings {
				t.Setenv(key, value)
			}
			t.Setenv(missing, "")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("missing %s must be rejected without printing its value: %v", missing, err)
			}
		})
	}
}

func TestEnabledSourceArtifactsLoadTOSConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "true")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-beijing.volces.com")
	t.Setenv("TOS_REGION", "cn-beijing")
	t.Setenv("TOS_BUCKET", "private-bucket")
	t.Setenv("TOS_ACCESS_KEY", "test-ak")
	t.Setenv("TOS_SECRET_KEY", "test-sk")
	t.Setenv("TOS_SECURITY_TOKEN", "test-session-token")
	t.Setenv("RAY_API_DEFAULT_IMAGE", "registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SourceArtifactsEnabled || cfg.TOSRegion != "cn-beijing" || cfg.TOSAccessKey != "test-ak" || cfg.TOSSecretKey != "test-sk" || cfg.TOSSecurityToken != "test-session-token" {
		t.Fatal("source artifact TOS configuration was not loaded")
	}
}

func TestEnabledSourceArtifactsRequirePinnedRayCLIDefaultImage(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "true")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-beijing.volces.com")
	t.Setenv("TOS_REGION", "cn-beijing")
	t.Setenv("TOS_BUCKET", "private-bucket")
	t.Setenv("TOS_ACCESS_KEY", "test-ak")
	t.Setenv("TOS_SECRET_KEY", "test-sk")
	t.Setenv("RAY_API_DEFAULT_IMAGE", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "RAY_API_DEFAULT_IMAGE") {
		t.Fatalf("source artifacts require a pinned Ray CLI default image: %v", err)
	}
}

func TestProductionSourceArtifactsRequireExplicitRayAPISpoolDir(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "true")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-beijing.volces.com")
	t.Setenv("TOS_REGION", "cn-beijing")
	t.Setenv("TOS_BUCKET", "private-bucket")
	t.Setenv("TOS_ACCESS_KEY", "test-ak")
	t.Setenv("TOS_SECRET_KEY", "test-sk")
	t.Setenv("RAY_API_SPOOL_DIR", "")
	cfg := Config{AppEnv: "production", SourceArtifactsEnabled: true, TOSEndpoint: "https://tos-cn-beijing.volces.com", TOSRegion: "cn-beijing", TOSBucket: "private-bucket", TOSAccessKey: "test-ak", TOSSecretKey: "test-sk", RayAPIDefaultImage: "registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SourceArtifactMaxPending: 1, SourceArtifactQuotaBytes: 2 * 1024 * 1024 * 1024}
	if err := validateSourceArtifactConfig(cfg); err == nil || !strings.Contains(err.Error(), "RAY_API_SPOOL_DIR") {
		t.Fatalf("missing production Ray spool dir must be rejected: %v", err)
	}
}

func TestDevelopmentRayAPISpoolDirDefaultsToTemporaryDirectory(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "false")
	t.Setenv("RAY_API_SPOOL_DIR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RayAPISpoolDir == "" {
		t.Fatal("development spool directory was not defaulted")
	}
}
