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
