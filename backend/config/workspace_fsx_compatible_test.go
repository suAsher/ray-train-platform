package config

import (
	"os"
	"strings"
	"testing"
)

func TestWorkspaceFSXCompatibleDefaultsDisabled(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATA_SPACES_ENABLED", "false")
	t.Setenv("WORKSPACE_FSX_COMPATIBLE_ENABLED", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceFSXCompatibleEnabled {
		t.Fatal("workspace compatibility must be opt-in")
	}
}

func TestWorkspaceFSXCompatibleRequiresDataSpaces(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATA_SPACES_ENABLED", "false")
	t.Setenv("WORKSPACE_FSX_COMPATIBLE_ENABLED", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "WORKSPACE_FSX_COMPATIBLE_ENABLED requires DATA_SPACES_ENABLED") {
		t.Fatalf("incompatible feature gates must fail closed: %v", err)
	}
}

func TestWorkspaceFSXCompatibleLoadsOnlyExplicitBoolean(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	t.Setenv("TOS_ENDPOINT", "https://tos.example.internal")
	t.Setenv("TOS_BUCKET", "workspace-test")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"workspace-test","server":"tos.example.internal","region":"cn-test"}`)
	t.Setenv("WORKSPACE_FSX_COMPATIBLE_ENABLED", "true")
	cfg, err := Load()
	if err != nil || !cfg.WorkspaceFSXCompatibleEnabled {
		t.Fatalf("explicit workspace compatibility was not enabled: %v", err)
	}
	t.Setenv("WORKSPACE_FSX_COMPATIBLE_ENABLED", "invalid")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WORKSPACE_FSX_COMPATIBLE_ENABLED") {
		t.Fatalf("invalid compatibility boolean must be rejected: %v", err)
	}
}

func TestWorkspaceFSXCompatibleChartAndBackendWiring(t *testing.T) {
	for _, contract := range []struct {
		path string
		text string
	}{
		{"../../helm/ray-train-platform/values.yaml", "workspaceCompatibleEnabled: false"},
		{"../../helm/ray-train-platform/templates/backend-deployment.yaml", "name: WORKSPACE_FSX_COMPATIBLE_ENABLED\n              value: {{ default false $dataSpaces.workspaceCompatibleEnabled | quote }}"},
		{"../main.go", "WorkspaceFSXCompatibleEnabled: cfg.WorkspaceFSXCompatibleEnabled"},
	} {
		contents, err := os.ReadFile(contract.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), contract.text) {
			t.Errorf("%s does not preserve workspace compatibility wiring %q", contract.path, contract.text)
		}
	}
}
