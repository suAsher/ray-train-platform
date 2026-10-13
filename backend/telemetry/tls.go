package telemetry

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"

	"ray-train-platform-backend/config"
)

const (
	scraperIdentity = "spiffe://observability.raytrain.local/victoriametrics/vmagent"
	backendIdentity = "spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry"
	metricsDNS      = "backend-metrics.ray-train-platform.svc"
)

// Read each handshake/export so projected Secret rotations take effect without
// restarting business requests. Invalid or partially rotated files fail closed;
// we deliberately do not retain an expired or revoked last-known-good identity.
func readTLSFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open telemetry TLS file")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
		return nil, errors.New("invalid telemetry TLS file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("cannot read telemetry TLS file")
	}
	return data, nil
}
func loadIdentity(caFile, certFile, keyFile string, usage x509.ExtKeyUsage) (tls.Certificate, *x509.CertPool, error) {
	var empty tls.Certificate
	ca, err := readTLSFile(caFile)
	if err != nil {
		return empty, nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return empty, nil, errors.New("invalid telemetry CA")
	}
	cert, err := readTLSFile(certFile)
	if err != nil {
		return empty, nil, err
	}
	key, err := readTLSFile(keyFile)
	if err != nil {
		return empty, nil, err
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return empty, nil, errors.New("invalid telemetry certificate/key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return empty, nil, errors.New("invalid telemetry certificate")
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		intermediate, err := x509.ParseCertificate(der)
		if err != nil {
			return empty, nil, errors.New("invalid telemetry certificate chain")
		}
		intermediates.AddCert(intermediate)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return empty, nil, errors.New("telemetry identity does not validate against configured CA")
	}
	if !hasIdentity(leaf, backendIdentity) {
		return empty, nil, errors.New("unexpected telemetry backend identity")
	}
	pair.Leaf = leaf
	return pair, roots, nil
}
func hasIdentity(cert *x509.Certificate, identity string) bool {
	return len(cert.URIs) == 1 && cert.URIs[0].String() == identity
}
func metricsServerTLS(cfg config.TelemetryConfig) (*tls.Config, error) {
	load := func() (*tls.Config, error) {
		pair, roots, err := loadIdentity(cfg.CAFile, cfg.MetricsCertFile, cfg.MetricsKeyFile, x509.ExtKeyUsageServerAuth)
		if err != nil {
			return nil, err
		}
		for _, dns := range []string{metricsDNS, metricsDNS + ".cluster.local"} {
			if pair.Leaf.VerifyHostname(dns) != nil {
				return nil, errors.New("telemetry metrics certificate must cover both service DNS names")
			}
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, SessionTicketsDisabled: true, VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || !hasIdentity(state.PeerCertificates[0], scraperIdentity) {
				return errors.New("unapproved telemetry scraper identity")
			}
			return nil
		}}, nil
	}
	initial, err := load()
	if err != nil {
		return nil, err
	}
	initial.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) { return load() }
	return initial, nil
}
