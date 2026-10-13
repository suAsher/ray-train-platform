package telemetry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ray-train-platform-backend/config"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func makeCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{parsed, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func issue(t *testing.T, ca testCA, dns []string, identity string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: dns, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	if identity != "" {
		uri, _ := url.Parse(identity)
		cert.URIs = []*url.URL{uri}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
func writePair(t *testing.T, certFile, keyFile string, cert, key []byte) {
	t.Helper()
	if err := os.WriteFile(certFile, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
}
func tlsFixture(t *testing.T) (config.TelemetryConfig, testCA) {
	t.Helper()
	dir := t.TempDir()
	ca := makeCA(t)
	cfg := config.TelemetryConfig{Enabled: true, MetricsAddr: ":9091", CAFile: filepath.Join(dir, "ca.crt"), MetricsCertFile: filepath.Join(dir, "tls.crt"), MetricsKeyFile: filepath.Join(dir, "tls.key"), OTLPCertFile: filepath.Join(dir, "tls.crt"), OTLPKeyFile: filepath.Join(dir, "tls.key"), OTLPEndpoint: "https://otel-gateway.opentelemetry.svc:14318"}
	if err := os.WriteFile(cfg.CAFile, ca.pem, 0600); err != nil {
		t.Fatal(err)
	}
	cert, key := issue(t, ca, []string{"backend-metrics.ray-train-platform.svc", "backend-metrics.ray-train-platform.svc.cluster.local"}, "spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry")
	writePair(t, cfg.MetricsCertFile, cfg.MetricsKeyFile, cert, key)
	return cfg, ca
}
func testClient(t *testing.T, ca testCA, identity string) *http.Client {
	t.Helper()
	cert, key := issue(t, ca, nil, identity)
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.pem)
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "backend-metrics.ray-train-platform.svc", Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}}}
}
func TestMetricsMTLSIdentityAndOnlyMetrics(t *testing.T) {
	cfg, ca := tlsFixture(t)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
	server := httptest.NewUnstartedServer(s.metricsHandler())
	server.TLS = s.metricsTLS
	server.StartTLS()
	defer server.Close()
	for _, tt := range []struct {
		name, identity, path string
		ok                   bool
		status               int
	}{
		{"scraper", "spiffe://observability.raytrain.local/victoriametrics/vmagent", "/metrics", true, 200},
		{"unknown path", "spiffe://observability.raytrain.local/victoriametrics/vmagent", "/healthz", true, 404},
		{"backend forbidden", "spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry", "/metrics", false, 0},
		{"URI suffix forbidden", "spiffe://observability.raytrain.local/victoriametrics/vmagent/evil", "/metrics", false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := testClient(t, ca, tt.identity).Get(server.URL + tt.path)
			if !tt.ok {
				if err == nil {
					resp.Body.Close()
					t.Fatal("accepted wrong identity")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.status {
				t.Fatalf("status %d", resp.StatusCode)
			}
		})
	}
	bad := testClient(t, makeCA(t), "spiffe://observability.raytrain.local/victoriametrics/vmagent")
	if resp, err := bad.Get(server.URL + "/metrics"); err == nil {
		resp.Body.Close()
		t.Fatal("accepted wrong CA")
	}
}
func TestTLSReloadRejectsInvalidFilesAndAcceptsRotation(t *testing.T) {
	cfg, ca := tlsFixture(t)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
	server := httptest.NewUnstartedServer(s.metricsHandler())
	server.TLS = s.metricsTLS
	server.StartTLS()
	defer server.Close()
	client := testClient(t, ca, "spiffe://observability.raytrain.local/victoriametrics/vmagent")
	getSerial := func() string {
		resp, err := client.Get(server.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.TLS.PeerCertificates[0].SerialNumber.String()
	}
	first := getSerial()
	cert, key := issue(t, ca, []string{"backend-metrics.ray-train-platform.svc", "backend-metrics.ray-train-platform.svc.cluster.local"}, "spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry")
	writePair(t, cfg.MetricsCertFile, cfg.MetricsKeyFile, cert, key)
	if first == getSerial() {
		t.Fatal("server certificate did not rotate")
	}
	if err := os.WriteFile(cfg.MetricsKeyFile, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Get(server.URL + "/metrics"); err == nil {
		resp.Body.Close()
		t.Fatal("invalid rotation must fail closed")
	}
	writePair(t, cfg.MetricsCertFile, cfg.MetricsKeyFile, cert, key)
	getSerial()
	if err := os.WriteFile(cfg.CAFile, makeCA(t).pem, 0600); err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Get(server.URL + "/metrics"); err == nil {
		resp.Body.Close()
		t.Fatal("old CA remained trusted")
	}
}
func TestNewFailsClosedBeforeListening(t *testing.T) {
	cfg, ca := tlsFixture(t)
	cert, key := issue(t, ca, []string{"wrong.example"}, "spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry")
	writePair(t, cfg.MetricsCertFile, cfg.MetricsKeyFile, cert, key)
	if s, err := New(cfg); err == nil {
		_ = s.Shutdown(t.Context())
		t.Fatal("accepted wrong server DNS")
	}
}

func TestSeparateMetricsListenerLifecycle(t *testing.T) {
 cfg,ca:=tlsFixture(t);cfg.MetricsAddr="127.0.0.1:9091";s,err:=New(cfg);if err!=nil{t.Fatal(err)};defer s.Shutdown(t.Context())
 if err:=s.Start();err!=nil{t.Fatal(err)}
 client:=testClient(t,ca,scraperIdentity);resp,err:=client.Get("https://127.0.0.1:9091/metrics");if err!=nil{t.Fatal(err)};resp.Body.Close();if resp.StatusCode!=200{t.Fatalf("metrics status %d",resp.StatusCode)}
 second,err:=New(cfg);if err!=nil{t.Fatal(err)};defer second.Shutdown(t.Context());if err:=second.Start();err==nil{t.Fatal("accepted occupied dedicated port")}
 if err:=s.Shutdown(t.Context());err!=nil{t.Fatal(err)}
}
