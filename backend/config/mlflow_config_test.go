package config

import "testing"

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
