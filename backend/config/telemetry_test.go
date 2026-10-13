package config

import (
	"strings"
	"testing"
)

func telemetryTestEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"TELEMETRY_ENABLED":           "true",
		"TELEMETRY_METRICS_ADDR":      "",
		"TELEMETRY_METRICS_CERT_FILE": "/etc/raytrain/telemetry/metrics.crt",
		"TELEMETRY_METRICS_KEY_FILE":  "/etc/raytrain/telemetry/metrics.key",
		"TELEMETRY_CA_FILE":           "/etc/raytrain/telemetry/ca.crt",
		"TELEMETRY_OTLP_CERT_FILE":    "/etc/raytrain/telemetry/client.crt",
		"TELEMETRY_OTLP_KEY_FILE":     "/etc/raytrain/telemetry/client.key",
		"TELEMETRY_OTLP_ENDPOINT":     "",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadTelemetryDisabledByDefaultIgnoresRuntimeConfiguration(t *testing.T) {
	for _, enabled := range []string{"", "false"} {
		t.Run("enabled="+enabled, func(t *testing.T) {
			telemetryTestEnvironment(t)
			t.Setenv("TELEMETRY_ENABLED", enabled)
			t.Setenv("TELEMETRY_METRICS_ADDR", "invalid-address")
			t.Setenv("TELEMETRY_OTLP_ENDPOINT", "http://unapproved.example:4318")
			t.Setenv("TELEMETRY_METRICS_KEY_FILE", "relative.key")
			cfg, err := LoadTelemetry()
			if err != nil || cfg != (TelemetryConfig{}) {
				t.Fatalf("disabled telemetry must ignore runtime settings: %#v, %v", cfg, err)
			}
		})
	}
}

func TestLoadTelemetryRejectsInvalidEnabledFlag(t *testing.T) {
	t.Setenv("TELEMETRY_ENABLED", "invalid-boolean")
	if _, err := LoadTelemetry(); err == nil || !strings.Contains(err.Error(), "TELEMETRY_ENABLED") {
		t.Fatalf("expected named flag validation error, got %v", err)
	}
}

func TestLoadTelemetryDefaultsDoNotReadCredentials(t *testing.T) {
	telemetryTestEnvironment(t)
	cfg, err := LoadTelemetry()
	if err != nil {
		t.Fatalf("configuration loading must not read certificate files: %v", err)
	}
	want := TelemetryConfig{
		Enabled:         true,
		MetricsAddr:     ":9091",
		MetricsCertFile: "/etc/raytrain/telemetry/metrics.crt",
		MetricsKeyFile:  "/etc/raytrain/telemetry/metrics.key",
		CAFile:          "/etc/raytrain/telemetry/ca.crt",
		OTLPCertFile:    "/etc/raytrain/telemetry/client.crt",
		OTLPKeyFile:     "/etc/raytrain/telemetry/client.key",
		OTLPEndpoint:    "https://otel-gateway.opentelemetry.svc:14318",
	}
	if cfg != want {
		t.Fatalf("unexpected telemetry configuration: %#v", cfg)
	}
}

func TestLoadTelemetryRequiresAbsoluteCertificateFilePaths(t *testing.T) {
	for _, key := range []string{"TELEMETRY_METRICS_CERT_FILE", "TELEMETRY_METRICS_KEY_FILE", "TELEMETRY_CA_FILE", "TELEMETRY_OTLP_CERT_FILE", "TELEMETRY_OTLP_KEY_FILE"} {
		for _, value := range []string{"", "relative.pem", "/", "/etc/certs/", "file:///etc/certs/client.pem", "/etc/certs/bad\n.pem"} {
			t.Run(key+"="+value, func(t *testing.T) {
				telemetryTestEnvironment(t)
				t.Setenv(key, value)
				if _, err := LoadTelemetry(); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("expected named certificate path validation error, got %v", err)
				}
			})
		}
	}
}

func TestLoadTelemetryRestrictsMetricsListenerToPort9091(t *testing.T) {
	for _, addr := range []string{":9091", "0.0.0.0:9091", "127.0.0.1:9091", "[::]:9091", "[::1]:9091"} {
		t.Run("allowed="+addr, func(t *testing.T) {
			telemetryTestEnvironment(t)
			t.Setenv("TELEMETRY_METRICS_ADDR", addr)
			cfg, err := LoadTelemetry()
			if err != nil || cfg.MetricsAddr != addr {
				t.Fatalf("approved listener rejected: %q, %v", addr, err)
			}
		})
	}
	for _, addr := range []string{":8080", ":9092", ":0", "9091", "localhost:9091", "0.0.0.0:09091", "https://0.0.0.0:9091", "127.0.0.1:9091/path"} {
		t.Run("rejected="+addr, func(t *testing.T) {
			telemetryTestEnvironment(t)
			t.Setenv("TELEMETRY_METRICS_ADDR", addr)
			if _, err := LoadTelemetry(); err == nil || !strings.Contains(err.Error(), "TELEMETRY_METRICS_ADDR") {
				t.Fatalf("expected metrics address validation error, got %v", err)
			}
		})
	}
}

func TestLoadTelemetryRestrictsOTLPEndpointToInternalMTLSGateway(t *testing.T) {
	for _, endpoint := range []string{
		"https://otel-gateway.opentelemetry.svc:14318",
		"https://otel-gateway.opentelemetry.svc:14318/",
		"https://otel-gateway.opentelemetry.svc.cluster.local:14318",
	} {
		t.Run("allowed="+endpoint, func(t *testing.T) {
			telemetryTestEnvironment(t)
			t.Setenv("TELEMETRY_OTLP_ENDPOINT", endpoint)
			cfg, err := LoadTelemetry()
			if err != nil || cfg.OTLPEndpoint != endpoint {
				t.Fatalf("approved OTLP endpoint rejected: %v", err)
			}
		})
	}
	for _, endpoint := range []string{
		"http://otel-gateway.opentelemetry.svc:14318",
		"https://otel-gateway.opentelemetry.svc:4318",
		"https://otel-gateway.opentelemetry.svc",
		"https://unapproved.example:14318",
		"https://otel-gateway.opentelemetry.svc.unapproved.example:14318",
		"https://127.0.0.1:14318",
		"https://otel-gateway.opentelemetry.svc:14318/v1/traces",
		"https://otel-gateway.opentelemetry.svc:14318/%2f",
		"https://otel-gateway.opentelemetry.svc:14318?token=must-not-echo",
		"https://otel-gateway.opentelemetry.svc:14318?",
		"https://otel-gateway.opentelemetry.svc:14318#must-not-echo",
		"https://otel-gateway.opentelemetry.svc:14318#",
		"https://must-not-echo@otel-gateway.opentelemetry.svc:14318",
		"https://otel-gateway.opentelemetry.svc:14318/%zzmust-not-echo",
	} {
		t.Run("rejected="+endpoint, func(t *testing.T) {
			telemetryTestEnvironment(t)
			t.Setenv("TELEMETRY_OTLP_ENDPOINT", endpoint)
			_, err := LoadTelemetry()
			if err == nil || !strings.Contains(err.Error(), "TELEMETRY_OTLP_ENDPOINT") {
				t.Fatalf("expected endpoint validation error, got %v", err)
			}
			if strings.Contains(err.Error(), "must-not-echo") {
				t.Fatal("endpoint validation error exposed configured credentials")
			}
		})
	}
}

func TestTelemetryConfigValidateProtectsDirectCallers(t *testing.T) {
	telemetryTestEnvironment(t)
	cfg, err := LoadTelemetry()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid direct configuration rejected: %v", err)
	}
	if err := (TelemetryConfig{Enabled: true}).Validate(); err == nil {
		t.Fatal("incomplete direct configuration accepted")
	}
	if err := (TelemetryConfig{MetricsAddr: "invalid", OTLPEndpoint: "invalid"}).Validate(); err != nil {
		t.Fatalf("disabled direct configuration must skip validation: %v", err)
	}
}

func TestLoadIncludesValidatedTelemetryConfiguration(t *testing.T) {
	telemetryTestEnvironment(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("MIGRATIONS_ONLY", "false")
	cfg, err := Load()
	if err != nil || !cfg.Telemetry.Enabled || cfg.Telemetry.MetricsAddr != ":9091" {
		t.Fatalf("application must load telemetry: %#v, %v", cfg.Telemetry, err)
	}
	t.Setenv("TELEMETRY_OTLP_ENDPOINT", "http://unapproved.example:4318")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TELEMETRY_OTLP_ENDPOINT") {
		t.Fatalf("application accepted invalid telemetry: %v", err)
	}
}

func TestLoadMigrationsOnlySkipsTelemetryValidation(t *testing.T) {
	t.Setenv("MIGRATIONS_ONLY", "true")
	t.Setenv("DATABASE_URL", "postgres://migration@db/platform")
	t.Setenv("TELEMETRY_ENABLED", "invalid-boolean")
	cfg, err := Load()
	if err != nil || !cfg.MigrationsOnly || cfg.Telemetry.Enabled {
		t.Fatalf("migrations must not load telemetry: %v", err)
	}
}
