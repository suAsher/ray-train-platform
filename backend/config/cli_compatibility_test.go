package config

import (
	"os"
	"strings"
	"testing"
)

func TestCLICompatibilityMinimumMustBeReleaseVersion(t *testing.T) {
	t.Setenv("PAT_PEPPER", testPATPepper)
	t.Setenv("SPK_RAYJOB_MINIMUM_VERSION", "dev")
	if _, err := Load(); err == nil {
		t.Fatal("invalid minimum accepted")
	}
	t.Setenv("SPK_RAYJOB_MINIMUM_VERSION", "release-20260906-02")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SPKRayjobMinimumVersion != "release-20260906-02" {
		t.Fatal("minimum not loaded")
	}
}

func TestChartWiresCLICompatibilityMinimum(t *testing.T) {
	values, err := os.ReadFile("../../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatalf("read chart values: %v", err)
	}
	if !strings.Contains(string(values), "spkRayjobMinimumVersion: \"\"") {
		t.Fatal("chart values must default CLI minimum compatibility to advisory-only")
	}
	template, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatalf("read backend deployment template: %v", err)
	}
	rendered := string(template)
	if !strings.Contains(rendered, "name: SPK_RAYJOB_MINIMUM_VERSION") || !strings.Contains(rendered, ".Values.backend.spkRayjobMinimumVersion") {
		t.Fatal("backend deployment must wire backend.spkRayjobMinimumVersion into SPK_RAYJOB_MINIMUM_VERSION")
	}
}
