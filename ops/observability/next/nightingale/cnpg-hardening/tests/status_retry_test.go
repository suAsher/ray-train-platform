package remote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudnative-pg/cloudnative-pg/internal/observabilitytest"
)

func TestObservabilityStatusRetriesTransientAuthWithoutDowngrade(t *testing.T) {
	authority := observabilitytest.NewAuthority(t, "retry-ca")
	serverIdentity := authority.Issue(t, "status", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	operator := authority.Issue(t, "operator", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, false)
	for _, test := range []struct {
		name      string
		firstCode int
		wantCalls int32
		wantError bool
	}{
		{name: "unregistered-fingerprint-recovers", firstCode: 503, wantCalls: 2},
		{name: "unauthorized-is-permanent", firstCode: 401, wantCalls: 1, wantError: true},
		{name: "not-found-is-permanent", firstCode: 404, wantCalls: 1, wantError: true},
		{name: "backend-failure-is-permanent", firstCode: 500, wantCalls: 1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			var anonymousCalls atomic.Int32
			server := observabilitytest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				call := calls.Add(1)
				if request.TLS == nil || len(request.TLS.PeerCertificates) != 1 ||
					!request.TLS.PeerCertificates[0].Equal(operator.Certificate) {
					anonymousCalls.Add(1)
				}
				if request.URL.Path != "/pg/status" {
					t.Errorf("unexpected status path: %q", request.URL.Path)
				}
				if call == 1 {
					http.Error(w, "operator certificate not registered", test.firstCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, "{}")
			}), &tls.Config{
				MinVersion:   tls.VersionTLS13,
				Certificates: []tls.Certificate{serverIdentity.TLS},
				ClientAuth:   tls.RequestClientCert,
			}, "")
			client := observabilitytest.Client(t, authority, &operator)
			transport := client.Transport.(*http.Transport).Clone()
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			client.Transport = transport
			t.Cleanup(transport.CloseIdleConnections)
			instanceClient := instanceClientImpl{Client: client}
			result := instanceClient.getReplicaStatusFromPodViaHTTP(context.Background(), corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "n9e-postgres-1"},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "postgres", Command: []string{"/controller/manager", "--status-port-tls"},
				}}},
				Status: corev1.PodStatus{PodIP: "127.0.0.1", Phase: corev1.PodRunning},
			})
			if (result.Error != nil) != test.wantError {
				t.Errorf("status error = %v, wantError=%v", result.Error, test.wantError)
			}
			if got := calls.Load(); got != test.wantCalls {
				t.Errorf("status requests = %d, want %d", got, test.wantCalls)
			}
			if anonymousCalls.Load() != 0 {
				t.Error("a retry dropped the operator certificate")
			}
		})
	}
}
