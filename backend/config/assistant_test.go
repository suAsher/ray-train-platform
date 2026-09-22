package config

import (
	"strings"
	"testing"
)

func TestAssistantDisabledIgnoresOptionalProviderConfiguration(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "false")
	t.Setenv("ASSISTANT_PROVIDERS", "not JSON")
	got, err := loadAssistantConfig()
	if err != nil || got.Enabled {
		t.Fatal(got, err)
	}
}
func TestAssistantRetrievalOnlyNeedsNoCredential(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "true")
	t.Setenv("ASSISTANT_PROVIDERS", "")
	t.Setenv("ASSISTANT_LOCAL_FIRST", "false")
	got, err := loadAssistantConfig()
	if err != nil || !got.Enabled || len(got.Routing.Providers) != 0 {
		t.Fatal(got.Enabled, err)
	}
}
func TestAssistantMultipleProvidersAndSecretReference(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "true")
	t.Setenv("ASSISTANT_LOCAL_FIRST", "true")
	t.Setenv("ASSISTANT_PROVIDER_COMPANY_KEY", "test-not-a-real-secret")
	t.Setenv("ASSISTANT_PROVIDERS", `[{"id":"company","kind":"api","baseURL":"https://llm.example.invalid/v1","model":"allowed-model","keyEnv":"ASSISTANT_PROVIDER_COMPANY_KEY"},{"id":"idle","kind":"local","baseURL":"http://serve.ai.svc.cluster.local:8000/v1","model":"local-model"}]`)
	got, err := loadAssistantConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Routing.LocalFirst || len(got.Routing.Providers) != 2 || got.Routing.Providers[0].APIKey != "test-not-a-real-secret" {
		t.Fatal("configuration was not resolved")
	}
}
func TestAssistantRejectsEmbeddedSecretsAndUnrelatedEnv(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "true")
	t.Setenv("ASSISTANT_LOCAL_FIRST", "false")
	for _, raw := range []string{
		`[{"apiKey":"do-not-echo-secret"}]`,
		`[{"id":"x","kind":"api","baseURL":"https://llm.example.invalid","model":"m","keyEnv":"DATABASE_URL"}]`,
		`[{"id":"x","kind":"api","baseURL":"https://llm.example.invalid","model":"m","keyEnv":"ASSISTANT_PROVIDER_MISSING_KEY"}]`,
		`[] {}`, `{}`,
	} {
		t.Setenv("ASSISTANT_PROVIDERS", raw)
		_, err := loadAssistantConfig()
		if err == nil || strings.Contains(err.Error(), "do-not-echo-secret") {
			t.Fatalf("configuration must reject without disclosing content: %v", err)
		}
	}
}

func TestAssistantNativeAnthropicConfiguration(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "true")
	t.Setenv("ASSISTANT_LOCAL_FIRST", "false")
	t.Setenv("ASSISTANT_PROVIDER_CLAUDE_KEY", "synthetic-claude-key")
	t.Setenv("ASSISTANT_PROVIDERS", `[{"id":"claude","kind":"api","protocol":"anthropic","baseURL":"https://api.anthropic.com","model":"configured-claude-model","keyEnv":"ASSISTANT_PROVIDER_CLAUDE_KEY"}]`)
	got, err := loadAssistantConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routing.Providers) != 1 || got.Routing.Providers[0].Protocol != "anthropic" {
		t.Fatal("native protocol not preserved")
	}
}

func TestAssistantIdleDemandAuthKeyIsOptionalButStrongWhenSet(t *testing.T) {
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("ASSISTANT_IDLE_DEMAND_AUTH_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("empty assistant demand key should keep route disabled: %v", err)
	}
	if cfg.AssistantIdleDemandAuthKey != "" {
		t.Fatal("empty assistant demand key was not preserved as disabled")
	}

	t.Setenv("ASSISTANT_IDLE_DEMAND_AUTH_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("short assistant demand key was accepted")
	}

	t.Setenv("ASSISTANT_IDLE_DEMAND_AUTH_KEY", "0123456789abcdef0123456789abcdef")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("strong assistant demand key was rejected: %v", err)
	}
	if cfg.AssistantIdleDemandAuthKey == "" {
		t.Fatal("strong assistant demand key was not loaded")
	}
}
