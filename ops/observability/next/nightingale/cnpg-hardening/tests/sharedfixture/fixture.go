// Package observabilitytest contains protocol fixtures injected into upstream
// internal/observabilitytest by the isolated verification runner, never the image.
package observabilitytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const MetricsURI = "spiffe://observability.raytrain.local/victoriametrics/vmagent"

type Authority struct {
	Certificate *x509.Certificate
	Key         *ecdsa.PrivateKey
	PEM         []byte
}

type Identity struct {
	TLS         tls.Certificate
	Certificate *x509.Certificate
	CertPEM     []byte
	KeyPEM      []byte
}

func NewAuthority(t testing.TB, name string) *Authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &Authority{Certificate: cert, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (a *Authority) Issue(t testing.TB, name string, usages []x509.ExtKeyUsage, uriStrings []string, expired bool) Identity {
	t.Helper()
	validUntil := time.Now().Add(time.Hour)
	if expired {
		validUntil = time.Now().Add(-time.Hour)
	}
	return a.IssueDuring(t, name, usages, uriStrings, time.Now().Add(-2*time.Hour), validUntil)
}

func (a *Authority) IssueDuring(t testing.TB, name string, usages []x509.ExtKeyUsage, uriStrings []string, validFrom, validUntil time.Time) Identity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    validFrom,
		NotAfter:     validUntil,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	for _, text := range uriStrings {
		uri, parseErr := url.Parse(text)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		template.URIs = append(template.URIs, uri)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.Certificate, &key.PublicKey, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	identity, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Identity{TLS: identity, Certificate: cert, CertPEM: certPEM, KeyPEM: keyPEM}
}

func (a *Authority) Pool(t testing.TB) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(a.PEM) {
		t.Fatal("fixture CA cannot be loaded")
	}
	return pool
}

func (i Identity) Fingerprint(t testing.TB) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(i.Certificate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(der)
	return hex.EncodeToString(digest[:])
}

func (i Identity) Write(t testing.TB, directory, name string) (certPath, keyPath string) {
	t.Helper()
	return WriteFile(t, directory, name+".crt", i.CertPEM), WriteFile(t, directory, name+".key", i.KeyPEM)
}

func WriteFile(t testing.TB, directory, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Serve preserves the production TLS config and HTTP handler while assigning an
// ephemeral test port. The optional peer is an accepted-connection fixture; it
// exercises net/http RemoteAddr propagation, not the cluster's SNAT behavior.
func Serve(t testing.TB, handler http.Handler, config *tls.Config, peer string) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = config.Clone()
	if peer != "" {
		server.Listener = peerListener{Listener: server.Listener, peer: peer}
	}
	// StartTLS installs httptest's fallback certificate. A raw TLS listener keeps
	// CNPG's GetCertificate callback as the sole server-certificate source.
	server.Listener = tls.NewListener(server.Listener, server.TLS)
	server.Start()
	server.URL = "https://" + server.Listener.Addr().String()
	t.Cleanup(server.Close)
	return server
}

func Client(t testing.TB, authority *Authority, identity *Identity) *http.Client {
	t.Helper()
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: authority.Pool(t), ServerName: "localhost"}
	if identity != nil {
		// Return the selected identity even when the server advertises another CA,
		// so wrong-CA tests exercise server verification instead of client filtering.
		config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &identity.TLS, nil
		}
	}
	transport := &http.Transport{TLSClientConfig: config, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	value, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type peerListener struct {
	net.Listener
	peer string
}

func (l peerListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return peerConnection{Conn: connection, peer: peerAddress(l.peer)}, nil
}

type peerConnection struct {
	net.Conn
	peer net.Addr
}

func (c peerConnection) RemoteAddr() net.Addr { return c.peer }

type peerAddress string

func (peerAddress) Network() string  { return "tcp" }
func (a peerAddress) String() string { return string(a) }
