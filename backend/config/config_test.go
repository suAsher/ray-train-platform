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
