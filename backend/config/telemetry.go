package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// TelemetryConfig contains operator-owned endpoints and certificate paths.
// Certificates are loaded by the telemetry runtime, never during config loading.
type TelemetryConfig struct {
	Enabled         bool
	MetricsAddr     string
	MetricsCertFile string
	MetricsKeyFile  string
	CAFile          string
	OTLPCertFile    string
	OTLPKeyFile     string
	OTLPEndpoint    string
}

func LoadTelemetry() (TelemetryConfig, error) {
	enabled, err := parseBool("TELEMETRY_ENABLED", false)
	if err != nil {
		return TelemetryConfig{}, fmt.Errorf("TELEMETRY_ENABLED must be a boolean")
	}
	if !enabled {
		return TelemetryConfig{}, nil
	}
	cfg := TelemetryConfig{
		Enabled:         true,
		MetricsAddr:     envOr("TELEMETRY_METRICS_ADDR", ":9091"),
		MetricsCertFile: os.Getenv("TELEMETRY_METRICS_CERT_FILE"),
		MetricsKeyFile:  os.Getenv("TELEMETRY_METRICS_KEY_FILE"),
		CAFile:          os.Getenv("TELEMETRY_CA_FILE"),
		OTLPCertFile:    os.Getenv("TELEMETRY_OTLP_CERT_FILE"),
		OTLPKeyFile:     os.Getenv("TELEMETRY_OTLP_KEY_FILE"),
		OTLPEndpoint:    envOr("TELEMETRY_OTLP_ENDPOINT", "https://otel-gateway.opentelemetry.svc:14318"),
	}
	if err := cfg.Validate(); err != nil {
		return TelemetryConfig{}, err
	}
	return cfg, nil
}

// Validate also protects callers that construct a configuration directly.
// It performs no network connections or credential-file reads.
func (cfg TelemetryConfig) Validate() error {
	if !cfg.Enabled {
		return nil
	}
	host, port, err := net.SplitHostPort(cfg.MetricsAddr)
	if err != nil || port != "9091" || (host != "" && net.ParseIP(host) == nil) {
		return fmt.Errorf("TELEMETRY_METRICS_ADDR must bind an IP address or all interfaces on port 9091")
	}
	if !validTelemetryOTLPEndpoint(cfg.OTLPEndpoint) {
		return fmt.Errorf("TELEMETRY_OTLP_ENDPOINT must use the internal otel-gateway HTTPS endpoint on port 14318 with only a root path")
	}
	for _, field := range []struct{ name, value string }{
		{"TELEMETRY_METRICS_CERT_FILE", cfg.MetricsCertFile},
		{"TELEMETRY_METRICS_KEY_FILE", cfg.MetricsKeyFile},
		{"TELEMETRY_CA_FILE", cfg.CAFile},
		{"TELEMETRY_OTLP_CERT_FILE", cfg.OTLPCertFile},
		{"TELEMETRY_OTLP_KEY_FILE", cfg.OTLPKeyFile},
	} {
		if !validTelemetryFilePath(field.value) {
			return fmt.Errorf("%s must be an absolute file path", field.name)
		}
	}
	return nil
}

func validTelemetryOTLPEndpoint(value string) bool {
	endpoint, err := url.Parse(value)
	if err != nil || endpoint.Scheme != "https" || endpoint.Opaque != "" || endpoint.User != nil {
		return false
	}
	if endpoint.Host != "otel-gateway.opentelemetry.svc:14318" && endpoint.Host != "otel-gateway.opentelemetry.svc.cluster.local:14318" {
		return false
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return false
	}
	return endpoint.RawPath == "" && endpoint.RawQuery == "" && !endpoint.ForceQuery && !strings.Contains(value, "#")
}

func validTelemetryFilePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && value != "/" &&
		!strings.HasSuffix(value, "/") && strings.IndexFunc(value, unicode.IsControl) == -1
}
