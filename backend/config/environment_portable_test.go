package config

import "testing"

func TestPortableEnvironmentBuildPlacementIsOperatorConfigured(t *testing.T) {
	environmentRegistryConfig(t)
	t.Setenv("ENVIRONMENT_STORAGE_CLASS", "")
	t.Setenv("ENVIRONMENT_NODE_SELECTOR_JSON", "")
	cfg, err := loadEnvironmentBuildConfig()
	if err != nil || cfg.StorageClass != "" || len(cfg.NodeSelector) != 0 {
		t.Fatalf("unexpected infrastructure defaults: %+v %v", cfg.NodeSelector, err)
	}
	t.Setenv("ENVIRONMENT_NODE_SELECTOR_JSON", `{"example.com/build-node":"true"}`)
	cfg, err = loadEnvironmentBuildConfig()
	if err != nil || cfg.NodeSelector["example.com/build-node"] != "true" {
		t.Fatalf("operator placement rejected: %v", err)
	}
	for _, invalid := range []string{`null`, `[]`, `{"bad key":"x"}`, `{"node":"bad value"}`} {
		t.Setenv("ENVIRONMENT_NODE_SELECTOR_JSON", invalid)
		if _, err := loadEnvironmentBuildConfig(); err == nil {
			t.Fatalf("invalid node selector accepted: %s", invalid)
		}
	}
}
