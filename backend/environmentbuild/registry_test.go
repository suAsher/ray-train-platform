package environmentbuild

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ray-train-platform-backend/registryauth"
)

// Keep the production client's fixed HTTPS destination and certificate checks;
// only the test transport dials the isolated in-process issuer. No Harbor
// credential or external network is involved.
func TestHarborRegistryAdapterPreservesIdentityPaginationAndExactRepositoryGrant(t *testing.T) {
	var deniedProjects, allowPush atomic.Bool
	allowPush.Store(true)
	credentials := Credentials{Username: "fixture-user", Secret: "fixture-only-secret"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != credentials.Username || secret != credentials.Secret || r.Host != RegistryHost {
			t.Error("adapter altered identity or trusted origin")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2.0/users/current":
			json.NewEncoder(w).Encode(map[string]any{"username": credentials.Username, "user_id": 10})
		case "/api/v2.0/projects":
			if deniedProjects.Load() {
				w.WriteHeader(403)
				return
			}
			if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("page_size") != "50" {
				t.Error("pagination changed")
			}
			json.NewEncoder(w).Encode([]map[string]any{{"name": "team", "project_id": 7, "current_user_role_id": 2}})
		case "/service/token":
			if !r.URL.Query().Has("scope") {
				if r.URL.Query().Get("account")!=credentials.Username || r.URL.Query().Get("service")!="harbor-registry" {t.Error("identity request changed")}
				now:=time.Now().Unix()
				payload,_:=json.Marshal(map[string]any{"iss":"harbor-token-issuer","sub":credentials.Username,"aud":"harbor-registry","exp":now+3600,"nbf":now,"iat":now,"access":nil})
				json.NewEncoder(w).Encode(map[string]string{"token":"e30."+base64.RawURLEncoding.EncodeToString(payload)+".fixture-signature"})
				return
			}
			if r.URL.Query().Get("scope") != "repository:team/nested/model:pull,push" {
				t.Error("repository scope changed")
			}
			actions := []string{"pull"}
			if allowPush.Load() {
				actions = append(actions, "push")
			}
			payload, _ := json.Marshal(map[string]any{"aud": "harbor-registry", "exp": time.Now().Add(time.Hour).Unix(), "access": []map[string]any{{"type": "repository", "name": "team/nested/model", "actions": actions}}})
			json.NewEncoder(w).Encode(map[string]string{"token": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture-signature"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: server.Certificate().DNSNames[0], MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	client := registryauth.NewClient()
	http.DefaultTransport = original
	defer transport.CloseIdleConnections()
	adapter := HarborRegistry{Client: client}
	ctx := context.Background()
	if err := adapter.Authenticate(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	projects, err := adapter.Projects(ctx, credentials, 2)
	if err != nil || len(projects) != 1 || projects[0].Name != "team" || projects[0].ProjectID != 7 || !projects[0].CanPush {
		t.Fatalf("project adapter mismatch: %+v %v", projects, err)
	}
	if err = adapter.CheckPush(ctx, credentials, "team/nested/model"); err != nil {
		t.Fatal(err)
	}
	allowPush.Store(false)
	if err = adapter.CheckPush(ctx, credentials, "team/nested/model"); !errors.Is(err, registryauth.ErrForbidden) {
		t.Fatal("pull-only grant accepted by adapter")
	}
	if err = adapter.CheckPush(ctx, credentials, "missing-project"); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing project accepted")
	}
	deniedProjects.Store(true)
	if _, err = adapter.Projects(ctx, credentials, 2); !errors.Is(err, registryauth.ErrProjectsUnavailable) {
		t.Fatal("project API denial was misclassified as a credential failure")
	}
	for _, formatted := range []string{credentials.String(), credentials.GoString()} {
		if strings.Contains(formatted, credentials.Secret) {
			t.Fatal("credential diagnostic leaks secret")
		}
	}
}
