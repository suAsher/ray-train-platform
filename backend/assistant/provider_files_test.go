package assistant

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalProviderUsesDedicatedCAAndBearerWithoutDisablingTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-local-key" {
			t.Error("local TLS/auth contract mismatch")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"verified model answer"}}]}`))
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	globalTLS := http.DefaultTransport.(*http.Transport).TLSClientConfig
	p, err := newHTTPProvider(ProviderConfig{ID: "idle", Kind: "local", BaseURL: server.URL, Model: "local", APIKey: "synthetic-local-key", CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	hp := p.(*httpProvider)
	transport := hp.client.Transport.(*http.Transport)
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 || transport.TLSClientConfig.RootCAs == nil || http.DefaultTransport.(*http.Transport).TLSClientConfig != globalTLS {
		t.Fatal("local trust changed global transport or disabled verification")
	}
	answer, err := p.complete(context.Background(), Input{Question: "test"})
	if err != nil || answer != "verified model answer" {
		t.Fatalf("trusted TLS call failed: %v", err)
	}
	// Another provider does not inherit the dedicated local CA.
	other, err := newHTTPProvider(ProviderConfig{ID: "company", Kind: "api", BaseURL: server.URL, Model: "company", APIKey: "synthetic-other-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.complete(context.Background(), Input{Question: "test"}); err == nil {
		t.Fatal("dedicated CA leaked to another provider")
	}
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "wrong-host.invalid"
	transport.CloseIdleConnections()
	if _, err := p.complete(context.Background(), Input{Question: "test"}); err == nil {
		t.Fatal("hostname verification was disabled")
	}
}

func TestProviderFileInputsAreBoundedAndErrorsDoNotDisclosePaths(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"empty": "", "spaces": " token ", "newline": "token\nnext", "large": strings.Repeat("x", 8193)} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadProviderKeyFile(path); err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("unsafe key input accepted or disclosed: %v", err)
		}
	}
	for _, path := range []string{dir, filepath.Join(dir, "missing"), "relative", dir + "/../escape"} {
		if _, err := ReadProviderKeyFile(path); err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("unsafe key path accepted or disclosed: %v", err)
		}
	}
	keyFile := filepath.Join(dir, "valid")
	if err := os.WriteFile(keyFile, []byte("synthetic-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "token")
	if err := os.Symlink(keyFile, link); err != nil {
		t.Fatal(err)
	}
	if key, err := ReadProviderKeyFile(link); err != nil || key != "synthetic-secret" {
		t.Fatal("Kubernetes-style projected file link rejected")
	}
	if _, err := providerTLSConfig(keyFile); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("non-certificate CA file accepted or disclosed")
	}
}
