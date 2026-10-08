package metricserver

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/internal/observabilitytest"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/management/postgres"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/management/postgres/webserver"
)

func TestObservabilityMetricsTLSProtocol(t *testing.T) {
	serverCA := observabilitytest.NewAuthority(t, "metrics-server-ca")
	clientCA := observabilitytest.NewAuthority(t, "metrics-client-ca")
	otherCA := observabilitytest.NewAuthority(t, "wrong-client-ca")
	serverIdentity := serverCA.Issue(t, "metrics", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	caPath := observabilitytest.WriteFile(t, t.TempDir(), "ca.crt", clientCA.PEM)
	t.Setenv("OBS_METRICS_CLIENT_CA_FILE", caPath)
	t.Setenv("OBS_METRICS_ALLOWED_CLIENT_URIS", observabilitytest.MetricsURI)
	t.Setenv("PGHOST", t.TempDir())
	production, err := newObservabilityMetricsServer(t, serverIdentity, true)
	if err != nil {
		t.Fatal(err)
	}
	config := webserver.ObservabilityTestHTTPServer(production.Webserver).TLSConfig
	if config == nil {
		t.Fatal("metricserver.New returned a plaintext listener")
	}
	if config.MinVersion != tls.VersionTLS13 || config.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("New TLS policy = version %x clientAuth %v, want TLS1.3 and RequireAndVerifyClientCert", config.MinVersion, config.ClientAuth)
	}
	if config.GetCertificate == nil {
		t.Error("New discarded the instance server-certificate callback")
	}
	server := serveObservabilityMetrics(t, production)
	tests := []struct {
		name      string
		authority *observabilitytest.Authority
		uris      []string
		expired   bool
		usage     x509.ExtKeyUsage
		allowed   bool
	}{
		{name: "no-certificate"},
		{name: "wrong-ca", authority: otherCA, uris: []string{observabilitytest.MetricsURI}, usage: x509.ExtKeyUsageClientAuth},
		{name: "wrong-namespace", authority: clientCA, uris: []string{"spiffe://observability.raytrain.local/other/vmagent"}, usage: x509.ExtKeyUsageClientAuth},
		{name: "wrong-role", authority: clientCA, uris: []string{"spiffe://observability.raytrain.local/observability-metrics/operator"}, usage: x509.ExtKeyUsageClientAuth},
		{name: "uri-suffix", authority: clientCA, uris: []string{observabilitytest.MetricsURI + "/extra"}, usage: x509.ExtKeyUsageClientAuth},
		{name: "multiple-uri-roles", authority: clientCA, uris: []string{observabilitytest.MetricsURI, "spiffe://observability.raytrain.local/observability-metrics/operator"}, usage: x509.ExtKeyUsageClientAuth},
		{name: "no-uri", authority: clientCA, usage: x509.ExtKeyUsageClientAuth},
		{name: "expired", authority: clientCA, uris: []string{observabilitytest.MetricsURI}, usage: x509.ExtKeyUsageClientAuth, expired: true},
		{name: "server-only-usage", authority: clientCA, uris: []string{observabilitytest.MetricsURI}, usage: x509.ExtKeyUsageServerAuth},
		{name: "monitoring-identity", authority: clientCA, uris: []string{observabilitytest.MetricsURI}, usage: x509.ExtKeyUsageClientAuth, allowed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var identity *observabilitytest.Identity
			if test.authority != nil {
				issued := test.authority.Issue(t, test.name, []x509.ExtKeyUsage{test.usage}, test.uris, test.expired)
				identity = &issued
			}
			assertObservabilityMetricsRequest(t, server, serverCA, identity, test.allowed)
		})
	}
}

func TestObservabilityMetricsRejectsInvalidConfiguration(t *testing.T) {
	authority := observabilitytest.NewAuthority(t, "metrics-ca")
	identity := authority.Issue(t, "metrics", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	directory := t.TempDir()
	caPath := observabilitytest.WriteFile(t, directory, "ca.crt", authority.PEM)
	brokenPath := observabilitytest.WriteFile(t, directory, "broken.crt", []byte("not a certificate"))
	emptyPath := observabilitytest.WriteFile(t, directory, "empty.crt", nil)
	leafPath := observabilitytest.WriteFile(t, directory, "leaf.crt", identity.CertPEM)
	corruptPrefixPath := observabilitytest.WriteFile(t, directory, "prefixed.crt", append([]byte("corrupt CA prefix\n"), authority.PEM...))
	t.Setenv("PGHOST", t.TempDir())
	for _, test := range []struct {
		name string
		tls  bool
		ca   string
		uris string
	}{
		{name: "tls-disabled", ca: caPath, uris: observabilitytest.MetricsURI},
		{name: "ca-unconfigured", tls: true, uris: observabilitytest.MetricsURI},
		{name: "ca-file-missing", tls: true, ca: filepath.Join(directory, "missing.crt"), uris: observabilitytest.MetricsURI},
		{name: "ca-file-corrupt", tls: true, ca: brokenPath, uris: observabilitytest.MetricsURI},
		{name: "ca-file-empty", tls: true, ca: emptyPath, uris: observabilitytest.MetricsURI},
		{name: "ca-file-is-leaf", tls: true, ca: leafPath, uris: observabilitytest.MetricsURI},
		{name: "ca-file-corrupt-prefix", tls: true, ca: corruptPrefixPath, uris: observabilitytest.MetricsURI},
		{name: "identity-unconfigured", tls: true, ca: caPath},
		{name: "identity-whitespace", tls: true, ca: caPath, uris: " "},
		{name: "identity-wildcard", tls: true, ca: caPath, uris: "spiffe://observability.raytrain.local/observability-metrics/*"},
		{name: "identity-malformed", tls: true, ca: caPath, uris: "vmagent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OBS_METRICS_CLIENT_CA_FILE", test.ca)
			t.Setenv("OBS_METRICS_ALLOWED_CLIENT_URIS", test.uris)
			server, err := newObservabilityMetricsServer(t, identity, test.tls)
			if err == nil || server != nil {
				t.Errorf("unsafe configuration returned a listener: server=%T error=%v", server, err)
			}
		})
	}
}

func TestObservabilityMetricsCARequiresControlledRestart(t *testing.T) {
	serverCA := observabilitytest.NewAuthority(t, "server-ca")
	oldCA := observabilitytest.NewAuthority(t, "old-client-ca")
	newCA := observabilitytest.NewAuthority(t, "new-client-ca")
	identity := serverCA.Issue(t, "metrics", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	oldClient := oldCA.Issue(t, "old-monitor", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{observabilitytest.MetricsURI}, false)
	newClient := newCA.Issue(t, "new-monitor", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{observabilitytest.MetricsURI}, false)
	directory := t.TempDir()
	caPath := observabilitytest.WriteFile(t, directory, "ca.crt", oldCA.PEM)
	t.Setenv("PGHOST", t.TempDir())
	t.Setenv("OBS_METRICS_CLIENT_CA_FILE", caPath)
	t.Setenv("OBS_METRICS_ALLOWED_CLIENT_URIS", observabilitytest.MetricsURI)
	original, err := newObservabilityMetricsServer(t, identity, true)
	if err != nil {
		t.Fatal(err)
	}
	oldServer := serveObservabilityMetrics(t, original)
	observabilitytest.WriteFile(t, directory, "ca.crt", newCA.PEM)
	assertObservabilityMetricsRequest(t, oldServer, serverCA, &oldClient, true)
	assertObservabilityMetricsRequest(t, oldServer, serverCA, &newClient, false)
	restarted, err := newObservabilityMetricsServer(t, identity, true)
	if err != nil {
		t.Fatal(err)
	}
	newServer := serveObservabilityMetrics(t, restarted)
	assertObservabilityMetricsRequest(t, newServer, serverCA, &oldClient, false)
	assertObservabilityMetricsRequest(t, newServer, serverCA, &newClient, true)
	observabilitytest.WriteFile(t, directory, "ca.crt", []byte("broken rotated CA"))
	if server, err := newObservabilityMetricsServer(t, identity, true); err == nil || server != nil {
		t.Error("corrupt replacement CA must fail construction without plaintext fallback")
	}
}

func newObservabilityMetricsServer(t *testing.T, identity observabilitytest.Identity, enabled bool) (*MetricsServer, error) {
	t.Helper()
	instance := postgres.NewInstance()
	instance.MetricsPortTLS = enabled
	instance.SetServerCertificate(&identity.TLS)
	instance.SetMightBeUnavailable(true)
	t.Cleanup(func() { instance.ConnectionPool().ShutdownConnections() })
	exporter := NewExporter(instance, nil)
	// Only the collector's database/cache dependency is isolated. New's real
	// registry, /metrics handler, TLS callback, and authentication stay intact.
	exporter.getCluster = func() (*apiv1.Cluster, error) { return nil, nil }
	return New(instance, exporter)
}

func serveObservabilityMetrics(t *testing.T, server *MetricsServer) *httptest.Server {
	t.Helper()
	production := webserver.ObservabilityTestHTTPServer(server.Webserver)
	if production.TLSConfig == nil {
		t.Fatal("metrics TLS config is missing")
	}
	return observabilitytest.Serve(t, production.Handler, production.TLSConfig, "")
}

func assertObservabilityMetricsRequest(t *testing.T, server *httptest.Server, serverCA *observabilitytest.Authority, identity *observabilitytest.Identity, allowed bool) {
	t.Helper()
	response, err := observabilitytest.Client(t, serverCA, identity).Get(server.URL + "/metrics")
	if !allowed {
		if err == nil {
			defer response.Body.Close()
			t.Errorf("unauthorized identity completed TLS and HTTP with status %d", response.StatusCode)
		}
		return
	}
	if err != nil {
		t.Fatalf("authorized metric scrape failed: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "go_goroutines") {
		t.Errorf("real /metrics handler did not return Prometheus metrics: status=%d body=%q", response.StatusCode, body)
	}
}
