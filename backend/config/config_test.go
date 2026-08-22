package config

import (
	"strings"
	"testing"
)

const testPATPepper = "0123456789abcdef0123456789abcdef"

func setValidProductionConfig(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://platform@db/platform")
	t.Setenv("OIDC_ISSUER_URL", "https://sso.example.com/realms/platform")
	t.Setenv("OIDC_CLIENT_ID", "ray-platform")
	t.Setenv("OIDC_AUDIENCE", "ray-platform-api")
	t.Setenv("KUBECONFIG", "/var/run/secrets/kubernetes.io/serviceaccount")
	t.Setenv("SOURCE_MATERIALIZER_IMAGE", "registry.example/source@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("WORKSPACE_IMAGE", "registry.example/workspace@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("RAY_IMAGE_ALLOWLIST", "registry.example/ray")
	t.Setenv("GIT_ALLOWLIST", "git.example.com")
	t.Setenv("DEMO_MODE", "false")
	t.Setenv("PAT_ENABLED", "true")
	t.Setenv("PAT_PEPPER", testPATPepper)
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-beijing.volces.com")
	t.Setenv("TOS_REGION", "cn-beijing")
	t.Setenv("TOS_BUCKET", "private-bucket")
	t.Setenv("TOS_ACCESS_KEY", "test-access-key")
	t.Setenv("TOS_SECRET_KEY", "test-secret-key")
	t.Setenv("RAY_API_SPOOL_DIR", "/var/lib/ray-platform/ray-packages")
	t.Setenv("RAY_API_DEFAULT_IMAGE", "registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}

func TestLoadRejectsProductionWithoutRequiredDependencies(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("OIDC_ISSUER_URL", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("PAT_PEPPER", testPATPepper)

	_, err := Load()
	if err == nil {
		t.Fatal("expected production configuration validation error")
	}
	if err.Error() != "DATABASE_URL is required in production" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsDemoModeInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("DEMO_MODE", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("expected demo mode validation error")
	}
	if err.Error() != "DEMO_MODE cannot be enabled in production" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsDisabledOIDCInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("OIDC_REQUIRED", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected production OIDC validation error")
	}
	if err.Error() != "OIDC_REQUIRED cannot be disabled in production" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAcceptsProductionConfiguration(t *testing.T) {
	setValidProductionConfig(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected valid production configuration: %v", err)
	}
	if cfg.AppEnv != "production" || !cfg.OIDCRequired || !cfg.PATEnabled || !cfg.SourceArtifactsEnabled {
		t.Fatal("unexpected production feature flags")
	}
}

func TestLoadUsesLokiGatewayByDefault(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOKI_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.LokiURL != "http://loki-gateway.loki.svc.cluster.local" {
		t.Fatalf("unexpected Loki default: %q", cfg.LokiURL)
	}
}

func TestLoadKeepsDataSpaceMountsDisabledUntilExplicitlyEnabled(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_BUCKET", "shanghai-data-transfer")
	t.Setenv("DATA_SPACES_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.DataSpacesEnabled {
		t.Fatal("data-space mounts must be disabled by default")
	}

	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`)
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	cfg, err = Load()
	if err != nil || !cfg.DataSpacesEnabled || cfg.DataSpacesMountCapacity != "1Ti" {
		t.Fatalf("explicit data-space enablement was not retained: cfg=%#v err=%v", cfg, err)
	}
	if cfg.DataSpacesPublicRoot != "ray-train/public/" {
		t.Fatalf("default public root = %q", cfg.DataSpacesPublicRoot)
	}
	t.Setenv("DATA_SPACES_PUBLIC_ROOT", "ray-train/tenants/local/datasets/public")
	cfg, err = Load()
	if err != nil || cfg.DataSpacesPublicRoot != "ray-train/tenants/local/datasets/public" {
		t.Fatalf("temporary public root was not retained: cfg=%#v err=%v", cfg, err)
	}
}

func TestLoadEnablesFinitePersonalObjectSetQuotaOnlyWithCompleteTOSConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_REGION", "cn-shanghai")
	t.Setenv("TOS_BUCKET", "shanghai-data-transfer")
	t.Setenv("TOS_ACCESS_KEY", "test-access-key")
	t.Setenv("TOS_SECRET_KEY", "test-secret-key")
	t.Setenv("TOS_OBJECT_SET_QUOTAS_ENABLED", "true")
	t.Setenv("PERSONAL_STORAGE_DEFAULT_QUOTA", "100Gi")
	t.Setenv("PERSONAL_STORAGE_MAX_QUOTA", "2Ti")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load ObjectSet quota configuration: %v", err)
	}
	if !cfg.TOSObjectSetQuotasEnabled || cfg.PersonalStorageDefaultQuotaBytes != 100*1024*1024*1024 || cfg.PersonalStorageMaxQuotaBytes != 2*1024*1024*1024*1024 {
		t.Fatalf("unexpected ObjectSet quota configuration: %#v", cfg)
	}

	t.Setenv("PERSONAL_STORAGE_DEFAULT_QUOTA", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PERSONAL_STORAGE_DEFAULT_QUOTA") {
		t.Fatalf("expected finite quota validation error, got %v", err)
	}
}

func TestLoadRejectsDataSpaceMountAgainstDifferentBackendTOS(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_BUCKET", "shanghai-data-transfer")
	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"other-bucket","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`)
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "same bucket and server") {
		t.Fatalf("expected data-space/backend TOS mismatch rejection, got %v", err)
	}
}

func TestLoadRejectsEnabledDataSpacesWithoutSecretlessFSXContract(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://s")
	t.Setenv("TOS_BUCKET", "b")
	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"b","server":"s","region":"r","secretName":"legacy"}`)
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON") {
		t.Fatalf("expected a secretless FSX contract error, got %v", err)
	}
}

func TestLoadRejectsIDCWithoutExistingClaim(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("IDC_STORAGE_ENABLED", "true")
	t.Setenv("IDC_EXISTING_CLAIM", "")
	t.Setenv("IDC_STORAGE_CLASS", "fast")
	if _, err := Load(); err == nil {
		t.Fatal("expected IDC existing claim validation error")
	}
}

func TestLoadAcceptsGovernedIDCDataSpacesWithoutLegacyClaim(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("IDC_STORAGE_ENABLED", "false")
	t.Setenv("IDC_DATA_SPACES_ENABLED", "true")
	t.Setenv("IDC_DATA_SPACES_MOUNT_CAPACITY", "1Pi")
	t.Setenv("IDC_DATA_SPACES_SOURCES_JSON", `{
      "original":{"server":"192.0.2.10","path":"/exports/original"},
      "wellspiking":{"server":"192.0.2.11","path":"/exports/wellspiking"},
      "shared":{"server":"storage.example.internal","path":"/exports/shared"}
    }`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load governed IDC data spaces: %v", err)
	}
	if !cfg.IDCDataSpacesEnabled || cfg.IDCDataSpaceSources["shared"].Path != "/exports/shared" {
		t.Fatalf("unexpected governed IDC configuration: %#v", cfg)
	}
}

func TestLoadRejectsIncompleteGovernedIDCDataSpaces(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("IDC_STORAGE_ENABLED", "false")
	t.Setenv("IDC_DATA_SPACES_ENABLED", "true")
	t.Setenv("IDC_DATA_SPACES_MOUNT_CAPACITY", "1Pi")
	t.Setenv("IDC_DATA_SPACES_SOURCES_JSON", `{"original":{"server":"192.0.2.10","path":"/exports/original"}}`)

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "wellspiking") {
		t.Fatalf("expected complete IDC source validation error, got %v", err)
	}
}

func TestLoadRejectsEnabledLocalCacheWithoutCompleteConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS", "")
	t.Setenv("LOCAL_CACHE_SIZE", "200Gi")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH", "/mnt/cache")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LOCAL_CACHE_STORAGE_CLASS") {
		t.Fatalf("expected local cache storage class validation error, got %v", err)
	}
}

func TestLoadAcceptsCompleteLocalCacheConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS", "ray-cache-local")
	t.Setenv("LOCAL_CACHE_SIZE", "200Gi")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH", "/mnt/cache")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load complete local cache configuration: %v", err)
	}
	if !cfg.LocalCacheEnabled || cfg.LocalCacheStorageClass != "ray-cache-local" || cfg.LocalCacheSize != "200Gi" || cfg.LocalCacheMountPath != "/mnt/cache" {
		t.Fatalf("unexpected local cache configuration: %#v", cfg)
	}
}

func TestLoadRejectsLocalCacheMountedInsideRayDefaultTempDirectory(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS", "ray-cache-local")
	t.Setenv("LOCAL_CACHE_SIZE", "200Gi")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH", "/tmp/ray")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LOCAL_CACHE_MOUNT_PATH") {
		t.Fatalf("expected unsafe Ray temp-directory cache mount to be rejected, got %v", err)
	}
}

func TestLoadPATDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "true")
	t.Setenv("PAT_PEPPER", testPATPepper)
	t.Setenv("PAT_DEFAULT_EXPIRY_DAYS", "")
	t.Setenv("PAT_MAX_EXPIRY_DAYS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load PAT defaults: %v", err)
	}
	if !cfg.PATEnabled || cfg.PATDefaultExpiryDays != 90 || cfg.PATMaxExpiryDays != 365 {
		t.Fatalf("unexpected PAT defaults: enabled=%t default=%d max=%d", cfg.PATEnabled, cfg.PATDefaultExpiryDays, cfg.PATMaxExpiryDays)
	}
}

func TestLoadRejectsMissingOrWeakPATPepperWhenEnabled(t *testing.T) {
	for _, pepper := range []string{"", "too-short"} {
		t.Run(pepper, func(t *testing.T) {
			setValidProductionConfig(t)
			t.Setenv("PAT_PEPPER", pepper)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "PAT_PEPPER") {
				t.Fatalf("expected PAT pepper validation error, got %v", err)
			}
		})
	}
}

func TestLoadAllowsDisabledPATWithoutPepperInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("PAT_PEPPER", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("disabled PAT must not require pepper: %v", err)
	}
	if cfg.PATEnabled {
		t.Fatal("PAT unexpectedly enabled")
	}
}

func TestLoadValidatesPATExpiryBounds(t *testing.T) {
	tests := []struct {
		name       string
		defaultDay string
		maxDay     string
	}{
		{name: "zero default", defaultDay: "0", maxDay: "365"},
		{name: "default above max", defaultDay: "91", maxDay: "90"},
		{name: "max above hard limit", defaultDay: "90", maxDay: "366"},
		{name: "invalid integer", defaultDay: "ninety", maxDay: "365"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "true")
			t.Setenv("PAT_PEPPER", testPATPepper)
			t.Setenv("PAT_DEFAULT_EXPIRY_DAYS", test.defaultDay)
			t.Setenv("PAT_MAX_EXPIRY_DAYS", test.maxDay)
			if _, err := Load(); err == nil {
				t.Fatal("expected PAT expiry validation error")
			}
		})
	}
}
