package observabilityauth

import (
	"crypto/tls"
	"crypto/x509"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudnative-pg/cloudnative-pg/internal/observabilitytest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const observabilityAPIServerIPs = "172.28.2.64,172.28.5.50"

func TestObservabilityWebhookSourceProtocol(t *testing.T) {
	authority := observabilitytest.NewAuthority(t, "webhook-server-ca")
	serverIdentity := authority.Issue(t, "webhook", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	tests := []struct {
		name       string
		peer       string
		path       string
		headers    map[string]string
		wantStatus int
		wantCalls  int32
	}{
		{name: "first-api-server", peer: "172.28.2.64:34567", path: "/validate", wantStatus: 202, wantCalls: 1},
		{name: "second-api-server", peer: "172.28.5.50:34567", path: "/mutate", wantStatus: 202, wantCalls: 1},
		{name: "mapped-first-api-server", peer: "[::ffff:172.28.2.64]:34567", path: "/validate", wantStatus: 202, wantCalls: 1},
		{name: "mapped-second-api-server", peer: "[::ffff:172.28.5.50]:34567", path: "/mutate", wantStatus: 202, wantCalls: 1},
		{name: "same-subnet", peer: "172.28.2.65:34567", path: "/validate", wantStatus: 403},
		{name: "other-subnet", peer: "172.28.5.51:34567", path: "/mutate", wantStatus: 403},
		{name: "loopback", peer: "127.0.0.1:34567", path: "/validate", wantStatus: 403},
		{name: "no-port", peer: "172.28.2.64", path: "/validate", wantStatus: 403},
		{name: "non-numeric-port", peer: "172.28.2.64:https", path: "/validate", wantStatus: 403},
		{name: "zero-port", peer: "172.28.2.64:0", path: "/validate", wantStatus: 403},
		{name: "out-of-range-port", peer: "172.28.2.64:65536", path: "/validate", wantStatus: 403},
		{name: "malformed", peer: "api-server.invalid:34567", path: "/validate", wantStatus: 403},
		{name: "spoofed-forwarding", peer: "172.28.2.65:34567", path: "/validate", wantStatus: 403,
			headers: map[string]string{
				"X-Forwarded-For": "172.28.2.64",
				"X-Real-IP":       "172.28.5.50",
				"Forwarded":       "for=172.28.2.64;proto=https",
			}},
		{name: "minimal-anonymous-ready", peer: "172.28.2.65:34567", path: "/readyz", wantStatus: 200},
		{name: "minimal-trusted-ready", peer: "172.28.2.64:34567", path: "/readyz", wantStatus: 200},
		{name: "ready-prefix-is-not-exempt", peer: "172.28.2.65:34567", path: "/readyz/validate", wantStatus: 403},
		{name: "ready-trailing-slash-is-not-exempt", peer: "172.28.2.65:34567", path: "/readyz/", wantStatus: 403},
		{name: "double-slash-is-not-exempt", peer: "172.28.2.65:34567", path: "//readyz", wantStatus: 403},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, "admission-handler-reached")
			})
			handler, err := NewWebhookHandler(backend, observabilityAPIServerIPs)
			if err != nil {
				t.Fatal(err)
			}
			server := observabilitytest.Serve(t, handler, &tls.Config{
				MinVersion:   tls.VersionTLS13,
				Certificates: []tls.Certificate{serverIdentity.TLS},
			}, test.peer)
			request, err := http.NewRequest(http.MethodGet, server.URL+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			response, err := observabilitytest.Client(t, authority, nil).Do(request)
			if err != nil {
				t.Fatalf("real TLS/HTTP request failed: %v", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.wantStatus {
				t.Errorf("status = %d, want %d; body=%q", response.StatusCode, test.wantStatus, body)
			}
			if got := calls.Load(); got != test.wantCalls {
				t.Errorf("admission handler calls = %d, want %d", got, test.wantCalls)
			}
			if test.path == "/readyz" && string(body) != "ok\n" {
				t.Errorf("readyz must have fixed minimal body, got %q", body)
			}
			if test.wantCalls == 0 && strings.Contains(string(body), "admission-handler-reached") {
				t.Error("protected admission response leaked")
			}
		})
	}
}

func TestObservabilityWebhookRejectsInvalidPolicy(t *testing.T) {
	for _, policy := range []string{
		"", " ", "172.28.2.0/24", "api-server.internal", "172.28.2.64:443",
		"172.28.2.64,172.28.5.50,172.28.2.65", "172.28.2.64,", ",172.28.5.50",
		"172.28.2.64,broken", "0.0.0.0", "*",
		"172.28.2.64", "172.28.2.64,172.28.2.64",
	} {
		t.Run(policy, func(t *testing.T) {
			handler, err := NewWebhookHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), policy)
			if err == nil || handler != nil {
				t.Errorf("invalid source policy %q must fail construction; handler=%T error=%v", policy, handler, err)
			}
		})
	}
}

func TestObservabilityWebhookAllRegistrationPathsAreGuarded(t *testing.T) {
	authority := observabilitytest.NewAuthority(t, "webhook-wrapper-ca")
	identity := authority.Issue(t, "webhook", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, false)
	for _, peer := range []string{"172.28.2.64:34567", "172.28.5.50:34567", "172.28.2.65:34567"} {
		t.Run(peer, func(t *testing.T) {
			wrapper, err := NewWebhookServer(webhook.Options{}, observabilityAPIServerIPs)
			if err != nil {
				t.Fatal(err)
			}
			var delegated atomic.Int32
			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				delegated.Add(1)
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, "protected-handler")
			})
			wrapper.Register("/registered", backend)
			wrapper.WebhookMux().Handle("/direct", backend)
			wrapper.WebhookMux().Handle("/readyz", backend)
			if wrapper.Options.WebhookMux == nil {
				t.Fatal("the actual listening server has no configured outer mux")
			}
			server := observabilitytest.Serve(t, wrapper.Options.WebhookMux, &tls.Config{
				MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{identity.TLS},
			}, peer)
			trusted := peer != "172.28.2.65:34567"
			for _, path := range []string{"/registered", "/direct", "/readyz"} {
				response, err := observabilitytest.Client(t, authority, nil).Get(server.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := http.StatusForbidden
				if trusted {
					want = http.StatusAccepted
				}
				if path == "/readyz" {
					want = http.StatusOK
					if string(body) != "ok\n" {
						t.Errorf("readyz bypassed the fixed minimal response: %q", body)
					}
				}
				if response.StatusCode != want {
					t.Errorf("%s via actual outer mux status=%d, want=%d", path, response.StatusCode, want)
				}
			}
			wantCalls := int32(0)
			if trusted {
				wantCalls = 2
			}
			if delegated.Load() != wantCalls {
				t.Errorf("registered/direct handler calls=%d, want=%d", delegated.Load(), wantCalls)
			}
		})
	}
}

func TestObservabilityWebhookControllerUsesGuardedServer(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "cmd", "manager", "controller", "controller.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var serverName string
	var configuredFromEnvironment bool
	ast.Inspect(file, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) == 0 || len(assignment.Rhs) != 1 {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "NewWebhookServer" {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if ok {
			serverName = name.Name
		}
		ast.Inspect(call, func(child ast.Node) bool {
			literal, ok := child.(*ast.BasicLit)
			if ok && literal.Value == `"OBS_WEBHOOK_API_SERVER_IPS"` {
				configuredFromEnvironment = true
			}
			return true
		})
		return true
	})
	var installed bool
	ast.Inspect(file, func(node ast.Node) bool {
		field, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, keyOK := field.Key.(*ast.Ident)
		value, valueOK := field.Value.(*ast.Ident)
		if keyOK && valueOK && key.Name == "WebhookServer" && serverName != "" && value.Name == serverName {
			installed = true
		}
		return true
	})
	if !configuredFromEnvironment || !installed {
		t.Error("controller must construct the guarded server from OBS_WEBHOOK_API_SERVER_IPS and install it in manager options")
	}
}
