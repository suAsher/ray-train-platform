package config

import (
	"strings"
	"testing"
)

func TestProductionLocalAuthenticationDoesNotNeedExternalIdentityOrStorage(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("OIDC_REQUIRED", "false")
	t.Setenv("OAUTH2_PROXY_AUTH_ENABLED", "false")
	t.Setenv("LOCAL_AUTH_ENABLED", "true")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "false")
	for _, name := range []string{"OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_AUDIENCE", "TOS_ENDPOINT", "TOS_REGION", "TOS_BUCKET", "TOS_ACCESS_KEY", "TOS_SECRET_KEY"} {
		t.Setenv(name, "")
	}
	if _, err := Load(); err != nil {
		t.Fatalf("standalone production rejected: %v", err)
	}
}

func TestGenericOIDCDoesNotEnableKeycloakBrowserIntegration(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("OIDC_REQUIRED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://identity.example.com/oauth2/default")
	t.Setenv("WEB_OIDC_ENABLED", "")
	cfg, err := Load()
	if err != nil || cfg.WebOIDCEnabled {
		t.Fatalf("generic OIDC must not implicitly enable Keycloak browser integration: %v", err)
	}
}

func TestWebOIDCRequiresValidBooleanAndOIDCAuthentication(t *testing.T) {
	for _, scenario := range []struct{ web, oidc, message string }{
		{"invalid", "true", "WEB_OIDC_ENABLED"},
		{"true", "false", "WEB_OIDC_ENABLED requires OIDC_REQUIRED"},
	} {
		t.Run(scenario.web+scenario.oidc, func(t *testing.T) {
			setValidProductionConfig(t)
			t.Setenv("OIDC_REQUIRED", scenario.oidc)
			t.Setenv("WEB_OIDC_ENABLED", scenario.web)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), scenario.message) {
				t.Fatalf("expected %s, got %v", scenario.message, err)
			}
		})
	}
}

func TestProductionLocalAuthenticationRequiresPATAndStrongPepper(t *testing.T) {
	for _, scenario := range []struct{ name, key, value, message string }{
		{"no PAT", "PAT_ENABLED", "false", "production requires"},
		{"weak pepper", "PAT_PEPPER", "short", "PAT_PEPPER"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			setValidProductionConfig(t)
			t.Setenv("OIDC_REQUIRED", "false")
			t.Setenv("OAUTH2_PROXY_AUTH_ENABLED", "false")
			t.Setenv("LOCAL_AUTH_ENABLED", "true")
			t.Setenv(scenario.key, scenario.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), scenario.message) {
				t.Fatalf("expected %q, got %v", scenario.message, err)
			}
		})
	}
}
