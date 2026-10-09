package webserver

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/internal/observabilitytest"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/management/postgres"
)

func TestObservabilityStatusAuthenticationProtocol(t *testing.T) {
	observabilityInClusterFixture(t)
	authority := observabilitytest.NewAuthority(t, "status-ca")
	serverIdentity := authority.Issue(t, "status", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	operator := authority.Issue(t, "operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, false)
	wrongOperator := authority.Issue(t, "wrong-operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, false)
	expired := authority.Issue(t, "expired-operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, true)
	future := authority.IssueDuring(t, "future-operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil,
		time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	for _, test := range []struct {
		name        string
		identity    *observabilitytest.Identity
		fingerprint string
		wantStatus  int
	}{
		{name: "anonymous", fingerprint: operator.Fingerprint(t), wantStatus: 401},
		{name: "unknown-operator", identity: &wrongOperator, fingerprint: operator.Fingerprint(t), wantStatus: 503},
		{name: "unregistered-operator", identity: &operator, wantStatus: 503},
		{name: "expired-pinned-operator", identity: &expired, fingerprint: expired.Fingerprint(t), wantStatus: 401},
		{name: "not-yet-valid-pinned-operator", identity: &future, fingerprint: future.Fingerprint(t), wantStatus: 401},
		{name: "registered-operator", identity: &operator, fingerprint: operator.Fingerprint(t), wantStatus: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := observabilityStatusInstance(t, serverIdentity, test.fingerprint)
			production, err := NewRemoteWebServer(instance, func() {}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if production.server.TLSConfig == nil {
				t.Fatal("status server TLS config missing")
			}
			if production.server.TLSConfig.ClientAuth != tls.RequestClientCert {
				t.Error("status must request certificates per endpoint and preserve anonymous kubelet probes")
			}
			server := observabilitytest.Serve(t, production.server.Handler, production.server.TLSConfig, "")
			response, err := observabilitytest.Client(t, authority, test.identity).Get(server.URL + "/pg/status")
			if err != nil {
				t.Fatalf("TLS/HTTP request failed before status handler: %v", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.wantStatus {
				t.Errorf("status = %d, want %d; body=%q", response.StatusCode, test.wantStatus, body)
			}
			if test.wantStatus == http.StatusOK {
				var status map[string]any
				if err := json.Unmarshal(body, &status); err != nil || len(status) == 0 {
					t.Errorf("authorized request did not reach actual PostgreSQL status handler: %q, error=%v", body, err)
				}
			} else if strings.Contains(string(body), "instanceManagerVersion") || strings.HasPrefix(string(body), "{") {
				t.Errorf("unauthorized detailed instance status leaked: %q", body)
			}
		})
	}
}

func TestObservabilityStatusFingerprintPropagation(t *testing.T) {
	observabilityInClusterFixture(t)
	authority := observabilitytest.NewAuthority(t, "status-ca")
	serverIdentity := authority.Issue(t, "status", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	first := authority.Issue(t, "first-operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, false)
	next := authority.Issue(t, "next-operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, false)
	instance := observabilityStatusInstance(t, serverIdentity, "")
	production, err := NewRemoteWebServer(instance, func() {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := observabilitytest.Serve(t, production.server.Handler, production.server.TLSConfig, "")
	for _, step := range []struct {
		fingerprint string
		identity    *observabilitytest.Identity
		wantStatus  int
	}{
		{identity: &first, wantStatus: 503},
		{fingerprint: first.Fingerprint(t), identity: &first, wantStatus: 200},
		{fingerprint: first.Fingerprint(t), identity: &next, wantStatus: 503},
		{fingerprint: next.Fingerprint(t), identity: &next, wantStatus: 200},
		{fingerprint: next.Fingerprint(t), identity: &first, wantStatus: 503},
		{fingerprint: next.Fingerprint(t), wantStatus: 401},
	} {
		instance.SetCluster(&apiv1.Cluster{Status: apiv1.ClusterStatus{OperatorCertificateFingerprint: step.fingerprint}})
		response, err := observabilitytest.Client(t, authority, step.identity).Get(server.URL + "/pg/status")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != step.wantStatus {
			t.Errorf("fingerprint propagation status=%d, want %d", response.StatusCode, step.wantStatus)
		}
	}
}

func TestObservabilityAnonymousHealthIsMinimal(t *testing.T) {
	observabilityInClusterFixture(t)
	authority := observabilitytest.NewAuthority(t, "health-ca")
	identity := authority.Issue(t, "status", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	instance := observabilityStatusInstance(t, identity, "")
	production, err := NewRemoteWebServer(instance, func() {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := observabilitytest.Serve(t, production.server.Handler, production.server.TLSConfig, "")
	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{path: "/healthz", status: 200, body: "OK"},
		{path: "/startupz", status: 200, body: "OK"},
		{path: "/readyz", status: 500, body: "unhealthy\n"},
		{path: "/failsafe", status: 200, body: "OK"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response, err := observabilitytest.Client(t, authority, nil).Get(server.URL + test.path)
			if err != nil {
				t.Fatalf("anonymous kubelet/failsafe TLS request failed: %v", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status || string(body) != test.body {
				t.Errorf("health status/body = %d %q, want %d %q", response.StatusCode, body, test.status, test.body)
			}
		})
	}
}

func observabilityStatusInstance(t *testing.T, identity observabilitytest.Identity, fingerprint string) *postgres.Instance {
	t.Helper()
	instance := postgres.NewInstance().WithNamespace("nightingale").WithClusterName("nightingale-postgres").WithPodName("nightingale-postgres-1")
	instance.StatusPortTLS = true
	instance.SetServerCertificate(&identity.TLS)
	instance.SetCluster(&apiv1.Cluster{Status: apiv1.ClusterStatus{OperatorCertificateFingerprint: fingerprint}})
	// This real GetStatus branch returns JSON without a database: no fake handler
	// replaces /pg/status. Replica liveness also avoids Kubernetes API calls.
	instance.PgRewindIsRunning = true
	instance.PgData = t.TempDir()
	observabilitytest.WriteFile(t, instance.PgData, "standby.signal", nil)
	return instance
}

func observabilityInClusterFixture(t *testing.T) {
	t.Helper()
	const tokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	token, err := os.ReadFile(tokenPath)
	if err != nil || strings.TrimSpace(string(token)) != "observability-protocol-fixture-only" {
		t.Fatal("protocol tests require the isolated runner's fake serviceaccount token; real cluster credentials are forbidden")
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "127.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "1")
}
