package config

import (
	"encoding/json"
	"os"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/environmentbuild"
	"strings"
	"testing"
)

func TestEnvironmentWorkspaceProfilesConfigAndChart(t *testing.T) {
	environmentRegistryConfig(t)
	profile := environmentbuild.WorkspaceProfile{WorkspaceImage: "registry.example.com/public/debug@sha256:" + strings.Repeat("b", 64), BaseImage: "registry.example.com/public/base@sha256:" + strings.Repeat("c", 64), PrepareImage: "registry.example.com/public/prepare@sha256:" + strings.Repeat("d", 64), Environment: domain.ImageEnvironment{CUDA: "12.6", Python: "3.10", PyTorch: "2.6.0+cu126"}}
	raw, _ := json.Marshal([]environmentbuild.WorkspaceProfile{profile})
	t.Setenv("ENVIRONMENT_WORKSPACE_PROFILES", string(raw))
	cfg, err := loadEnvironmentBuildConfig()
	if err != nil || len(cfg.WorkspaceProfiles) != 1 || cfg.WorkspaceProfiles[0] != profile {
		t.Fatalf("profile configuration lost: %+v %v", cfg.WorkspaceProfiles, err)
	}
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil || !strings.Contains(string(template), "ENVIRONMENT_WORKSPACE_PROFILES") || !strings.Contains(string(template), "$environmentBuilds.workspaceProfiles") {
		t.Fatal("chart does not render profiles JSON")
	}
	for _, value := range []string{"{}", "null", "[{}]", `[{"workspaceImage":"latest"}]`, string(raw) + " trailing"} {
		t.Setenv("ENVIRONMENT_WORKSPACE_PROFILES", value)
		if _, err := loadEnvironmentBuildConfig(); err == nil {
			t.Fatalf("accepted profile configuration %s", value)
		}
	}
}
