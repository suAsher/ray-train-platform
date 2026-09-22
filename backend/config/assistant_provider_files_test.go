package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantLocalProviderSecretFileConfiguration(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "true")
	t.Setenv("ASSISTANT_LOCAL_FIRST", "true")
	keyFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(keyFile, []byte("synthetic-inference-secret\n"), 0600); err != nil { t.Fatal(err) }
	raw, _ := json.Marshal([]map[string]any{{"id":"idle", "kind":"local", "baseURL":"https://assistant.ai.svc.cluster.local:8443/v1", "model":"local-model", "keyFile":keyFile}})
	t.Setenv("ASSISTANT_PROVIDERS", string(raw))
	got, err := loadAssistantConfig()
	if err != nil || len(got.Routing.Providers) != 1 { t.Fatalf("Secret file configuration failed: %v", err) }
	if got.Routing.Providers[0].APIKey != "synthetic-inference-secret" { t.Fatal("Secret file was not resolved") }
}

func TestAssistantProviderRejectsUnsafeSecretReferences(t *testing.T) {
	t.Setenv("ASSISTANT_ENABLED", "true")
	t.Setenv("ASSISTANT_LOCAL_FIRST", "false")
	t.Setenv("ASSISTANT_PROVIDER_TEST_KEY", "synthetic-test-key")
	for _, fields := range []map[string]any{
		{"keyFile":"relative-token"},
		{"keyFile":"/missing/credential"},
		{"keyFile":"/missing/credential", "keyEnv":"ASSISTANT_PROVIDER_TEST_KEY"},
		{"caFile":"relative-ca"},
		{"baseURL":"http://assistant.ai.svc.cluster.local:8000/v1", "caFile":"/missing/ca"},
	} {
		entry := map[string]any{"id":"idle", "kind":"local", "baseURL":"https://assistant.ai.svc.cluster.local:8443/v1", "model":"local-model"}
		for key,value := range fields { entry[key] = value }
		raw, _ := json.Marshal([]map[string]any{entry})
		t.Setenv("ASSISTANT_PROVIDERS", string(raw))
		if _, err := loadAssistantConfig(); err == nil || strings.Contains(err.Error(), "/missing/") || strings.Contains(err.Error(), "synthetic-test-key") { t.Fatalf("unsafe configuration accepted or disclosed: %v", err) }
	}
}
