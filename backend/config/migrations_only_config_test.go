package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadProductionMigrationsOnlyRequiresOnlyDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("MIGRATIONS_ONLY", "true")
	t.Setenv("DATABASE_URL", "postgres://migration@db/platform")
	t.Setenv("OIDC_REQUIRED", "not-a-runtime-boolean")
	t.Setenv("PAT_ENABLED", "true")
	t.Setenv("PAT_PEPPER", "")
	t.Setenv("SOURCE_ARTIFACTS_ENABLED", "true")
	t.Setenv("TOS_ENDPOINT", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("SOURCE_MATERIALIZER_IMAGE", "")
	t.Setenv("WORKSPACE_IMAGE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("migration-only config must skip runtime validation: %v", err)
	}
	if !cfg.MigrationsOnly || cfg.DatabaseURL == "" {
		t.Fatal("migration-only database configuration was not retained")
	}
}

func TestLoadProductionMigrationsOnlyRejectsMissingDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("MIGRATIONS_ONLY", "true")
	t.Setenv("DATABASE_URL", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected migration database requirement, got %v", err)
	}
}

func TestChartDelegatesMigrationsToBackendStartup(t *testing.T) {
	contents, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("Helm chart is not mounted with the backend package")
		}
		t.Fatalf("read backend deployment template: %v", err)
	}
	template := string(contents)
	if !strings.Contains(template, "name: DATABASE_URL") {
		t.Fatal("backend deployment must receive the database URL for startup migrations")
	}
	if strings.Contains(template, "helm.sh/hook") || strings.Contains(template, "MIGRATIONS_ONLY") {
		t.Fatal("Chart must not run migrations as a pre-install hook; test PostgreSQL is created by the same release")
	}
}
