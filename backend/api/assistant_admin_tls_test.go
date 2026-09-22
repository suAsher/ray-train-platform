package api

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantAdminStatusTLSUsesOnlyConfiguredCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	client := newAssistantStatusHTTPClient(path)
	if client == nil {
		t.Fatal("valid CA rejected")
	}
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("unsafe TLS transport")
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("trusted TLS failed: %v", err)
	}
	response.Body.Close()
	// A client without the configured CA must still reject this private server.
	if response, err := newAssistantStatusHTTPClient().Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("private CA changed global trust")
	}
	for _, bad := range []string{filepath.Dir(path), filepath.Join(t.TempDir(), "missing"), "relative-ca.crt"} {
		if newAssistantStatusHTTPClient(bad) != nil {
			t.Fatal("unsafe CA accepted")
		}
	}
	if err := os.WriteFile(path, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if newAssistantStatusHTTPClient(path) != nil {
		t.Fatal("invalid CA accepted")
	}
}

func TestAssistantAdminStatusUsesFixedHTTPSPathWhenCAConfigured(t *testing.T) {
	h := NewHandler(&fakeJobRepository{}, Options{AssistantControllerCAFile: "/missing/ca.crt"})
	if h.assistantStatusHTTP != nil {
		t.Fatal("missing CA must fail closed")
	}
	h.assistantStatusHTTP = &http.Client{Transport: assistantStatusRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://assistant-idle-controller.raytrain-assistant-test.svc.cluster.local:8443/status" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected controller destination or credential")
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})}
	got, reason := h.readAssistantControllerStatus(context.Background(), "raytrain-assistant-test")
	if reason != "controller_unavailable" || got.Gate.Allow {
		t.Fatal("failed status observation opened gate")
	}
}

func TestAssistantAdminStatusMissingCARejectsLegacyHTTPFallback(t *testing.T) {
    h := NewHandler(&fakeJobRepository{}, Options{})
    h.assistantStatusHTTP = &http.Client{Transport:assistantStatusRoundTrip(func(*http.Request) (*http.Response,error) {
        t.Fatal("missing CA must not contact any legacy endpoint")
        return nil, nil
    })}
    got, reason := h.readAssistantControllerStatus(context.Background(), "raytrain-assistant-test")
    if reason != "controller_unavailable" || got.Gate.Allow { t.Fatal("missing CA allowed status observation") }
}
