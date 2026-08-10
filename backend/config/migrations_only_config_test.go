package config

import (
	"os"
	"regexp"
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

func TestMigrationJobInjectsOnlyDatabaseConfiguration(t *testing.T) {
	contents, err := os.ReadFile("../../helm/ray-train-platform/templates/migrations-job.yaml")
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("Helm chart is not mounted with the backend package")
		}
		t.Fatalf("read migration Job template: %v", err)
	}
	template := string(contents)
	for _, required := range []string{"name: MIGRATIONS_ONLY", "name: DATABASE_URL"} {
		if !strings.Contains(template, required) {
			t.Fatalf("migration Job missing %q", required)
		}
	}
	for _, forbidden := range []string{"PAT_PEPPER", "TOS_ACCESS_KEY", "TOS_SECRET_KEY"} {
		if strings.Contains(template, forbidden) {
			t.Fatalf("migration Job injects runtime secret %s", forbidden)
		}
	}
	names := regexp.MustCompile(`(?m)^\s*- name: ([A-Z0-9_]+)\s*$`).FindAllStringSubmatch(template, -1)
	allowed := map[string]bool{"APP_ENV": true, "MIGRATIONS_ONLY": true, "DATABASE_URL": true}
	for _, match := range names {
		if !allowed[match[1]] {
			t.Fatalf("migration Job injects runtime-only environment variable %s", match[1])
		}
	}
}
