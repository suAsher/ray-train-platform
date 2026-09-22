package assistantidle

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPDemandObserverRequiresFreshValidAuthenticatedTLSObservation(t *testing.T) {
	now := time.Now().UTC()
	token := strings.Repeat("a", 64)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body    string
		status        int
		want, wantErr bool
	}{
		{"idle", fmt.Sprintf(`{"success":true,"data":{"hasDemand":false,"gpuCount":0,"trainingJobCount":0,"workspaceCount":0,"observedAt":%q}}`, now.Format(time.RFC3339Nano)), 200, false, false},
		{"pending", fmt.Sprintf(`{"success":true,"data":{"hasDemand":true,"gpuCount":1,"trainingJobCount":1,"workspaceCount":0,"observedAt":%q}}`, now.Format(time.RFC3339Nano)), 200, true, false},
		{"stale", fmt.Sprintf(`{"success":true,"data":{"hasDemand":false,"observedAt":%q}}`, now.Add(-5*time.Second).Format(time.RFC3339Nano)), 200, false, true},
		{"missing demand", fmt.Sprintf(`{"success":true,"data":{"observedAt":%q}}`, now.Format(time.RFC3339Nano)), 200, false, true},
		{"inconsistent", fmt.Sprintf(`{"success":true,"data":{"hasDemand":false,"gpuCount":1,"observedAt":%q}}`, now.Format(time.RFC3339Nano)), 200, false, true},
		{"unavailable", `{}`, 503, false, true}, {"redirect", `{}`, 302, false, true}, {"invalid", `{`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("invalid request contract")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			observer, err := NewHTTPDemandObserver(file, "")
			if err != nil {
				t.Fatal(err)
			}
			observer.url = server.URL
			observer.now = func() time.Time { return now }
			if _, err := observer.Pending(context.Background()); err == nil {
				t.Fatal("untrusted TLS accepted")
			}
			observer.client.Transport = server.Client().Transport
			got, err := observer.Pending(context.Background())
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("pending=%t err=%v", got, err)
			}
		})
	}
}

func TestHTTPDemandObserverDisablesEnvironmentProxyButKeepsTLSAndRedirectPolicy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	token := strings.Repeat("b", 64)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	observer, err := NewHTTPDemandObserver(file, "")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := observer.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", observer.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("assistant demand observer must not inherit environment proxy settings")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("assistant demand observer lost TLS policy: %+v", transport.TLSClientConfig)
	}
	request := httptest.NewRequest(http.MethodGet, "https://raytrain.wellspiking.ai/redirect", nil)
	if err := observer.client.CheckRedirect(request, []*http.Request{request}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("assistant demand observer redirect policy changed: %v", err)
	}
}
