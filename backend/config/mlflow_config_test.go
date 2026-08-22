package config

import (
	"strings"
	"testing"
)

func TestLoadMLflowIsOptInAndValidatesInternalURL(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")

	cfg, err := Load()
	if err != nil || cfg.MLflowEnabled {
		t.Fatalf("MLflow must remain disabled by default: cfg=%#v err=%v", cfg, err)
	}

	t.Setenv("MLFLOW_ENABLED", "true")
	t.Setenv("PAT_PEPPER", "0123456789abcdef0123456789abcdef")
	t.Setenv("MLFLOW_TRACKING_URL", "http://mlflow.mlflow-system.svc.cluster.local:5000")
	t.Setenv("MLFLOW_INGEST_URL", "http://mlflow-ingest.mlflow-system.svc.cluster.local:8080")
	t.Setenv("MLFLOW_EXPERIMENT_PREFIX", "raytrain")
	cfg, err = Load()
	if err != nil || !cfg.MLflowEnabled || cfg.MLflowIngestURL != "http://mlflow-ingest.mlflow-system.svc.cluster.local:8080" || cfg.MLflowExperimentPrefix != "raytrain" {
		t.Fatalf("expected valid MLflow configuration: cfg=%#v err=%v", cfg, err)
	}

	t.Setenv("MLFLOW_TRACKING_URL", "https://mlflow.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("workloads must not be configured to send metrics to an arbitrary external URL")
	}

	t.Setenv("MLFLOW_TRACKING_URL", "http://mlflow.mlflow-system.svc.cluster.local:5000")
	t.Setenv("MLFLOW_INGEST_URL", "https://mlflow.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("workload ingest must remain behind an in-cluster write-only gateway")
	}
}

func TestLoadMLflowDashboardDefaultsDisabledWithoutChangingMLflowRequirements(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("MLFLOW_DASHBOARD_ENABLED", "false")
	t.Setenv("MLFLOW_PUBLIC_ORIGIN", "http://not-used.example/path")
	t.Setenv("MLFLOW_DASHBOARD_SESSION_HOURS", "99")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("disabled dashboard must preserve existing configuration behavior: %v", err)
	}
	if cfg.MLflowDashboardEnabled {
		t.Fatal("MLflow dashboard must be disabled by default")
	}
	if cfg.MLflowDashboardSessionHours != 8 {
		t.Fatalf("dashboard session hours = %d, want default 8", cfg.MLflowDashboardSessionHours)
	}
}

func TestLoadMLflowDashboardValidatesDependenciesAndPublicOrigin(t *testing.T) {
	setValidMLflowDashboardEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load valid MLflow dashboard configuration: %v", err)
	}
	if !cfg.MLflowDashboardEnabled || cfg.MLflowPublicOrigin != "https://portal.example.com:8443" || cfg.MLflowDashboardSessionHours != 8 {
		t.Fatalf("unexpected dashboard configuration: %#v", cfg)
	}

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "MLflow disabled", key: "MLFLOW_ENABLED", value: "false"},
		{name: "tracking URL absent", key: "MLFLOW_TRACKING_URL", value: ""},
		{name: "weak pepper", key: "PAT_PEPPER", value: strings.Repeat("p", 31)},
		{name: "HTTP public origin", key: "MLFLOW_PUBLIC_ORIGIN", value: "http://portal.example.com"},
		{name: "origin credentials", key: "MLFLOW_PUBLIC_ORIGIN", value: "https://user@portal.example.com"},
		{name: "origin path", key: "MLFLOW_PUBLIC_ORIGIN", value: "https://portal.example.com/mlflow"},
		{name: "origin query", key: "MLFLOW_PUBLIC_ORIGIN", value: "https://portal.example.com?x=1"},
		{name: "origin fragment", key: "MLFLOW_PUBLIC_ORIGIN", value: "https://portal.example.com#x"},
		{name: "session too short", key: "MLFLOW_DASHBOARD_SESSION_HOURS", value: "0"},
		{name: "session too long", key: "MLFLOW_DASHBOARD_SESSION_HOURS", value: "25"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidMLflowDashboardEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid dashboard configuration accepted")
			}
		})
	}
}

func setValidMLflowDashboardEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("MLFLOW_ENABLED", "true")
	t.Setenv("MLFLOW_TRACKING_URL", "http://mlflow.mlflow-system.svc.cluster.local:5000")
	t.Setenv("MLFLOW_INGEST_URL", "http://mlflow-ingest.mlflow-system.svc.cluster.local:8080")
	t.Setenv("MLFLOW_EXPERIMENT_PREFIX", "raytrain")
	t.Setenv("PAT_PEPPER", strings.Repeat("p", 32))
	t.Setenv("MLFLOW_DASHBOARD_ENABLED", "true")
	t.Setenv("MLFLOW_PUBLIC_ORIGIN", "https://portal.example.com:8443")
	t.Setenv("MLFLOW_DASHBOARD_SESSION_HOURS", "8")
}
