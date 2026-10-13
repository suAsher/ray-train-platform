package telemetry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"ray-train-platform-backend/config"
)

type rotatingTransport struct {
	cfg config.TelemetryConfig
	// The dial hook exists for isolated TLS integration tests; production always
	// uses normal DNS resolution and never overrides certificate verification.
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func (t *rotatingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	endpoint, _ := url.Parse(t.cfg.OTLPEndpoint)
	if request.URL.Scheme != "https" || request.URL.Host != endpoint.Host || (request.URL.Path != "/v1/traces" && request.URL.Path != "/v1/logs") || request.URL.RawQuery != "" {
		return nil, errors.New("unapproved telemetry export destination")
	}
	pair, roots, err := loadIdentity(t.cfg.CAFile, t.cfg.OTLPCertFile, t.cfg.OTLPKeyFile, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: t.dialContext, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSHandshakeTimeout: exportTimeout, ResponseHeaderTimeout: exportTimeout, MaxResponseHeaderBytes: 16 << 10, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{pair}, ServerName: endpoint.Hostname()}}
	defer transport.CloseIdleConnections()
	clone := request.Clone(request.Context())
	clone.Header = make(http.Header)
	clone.Header.Set("Content-Type", "application/x-protobuf")
	return transport.RoundTrip(clone)
}
func exporters(cfg config.TelemetryConfig, transport http.RoundTripper) (sdktrace.SpanExporter, sdklog.Exporter, error) {
	client := &http.Client{Transport: transport, Timeout: exportTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimSuffix(cfg.OTLPEndpoint, "/")
	traces, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(base+"/v1/traces"), otlptracehttp.WithHTTPClient(client), otlptracehttp.WithHeaders(map[string]string{}), otlptracehttp.WithCompression(otlptracehttp.NoCompression), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, nil, errors.New("cannot initialize telemetry trace exporter")
	}
	logs, err := otlploghttp.New(context.Background(), otlploghttp.WithEndpointURL(base+"/v1/logs"), otlploghttp.WithHTTPClient(client), otlploghttp.WithHeaders(map[string]string{}), otlploghttp.WithCompression(otlploghttp.NoCompression), otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: false}))
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = traces.Shutdown(ctx)
		return nil, nil, errors.New("cannot initialize telemetry log exporter")
	}
	return traces, logs, nil
}
