package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ray-train-platform-backend/config"
)

func TestNewOIDCValidatorSkipsDiscoveryWhenOIDCIsNotRequired(t *testing.T) {
	var discoveryCalls atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveryCalls.Add(1)
		http.Error(w, "unexpected discovery", http.StatusInternalServerError)
	}))
	defer issuer.Close()

	validator, err := newOIDCValidator(config.Config{
		OIDCRequired:  false,
		OIDCIssuerURL: issuer.URL,
		OIDCClientID:  "development-client",
		OIDCAudience:  "development-audience",
		DemoMode:      true,
	})
	if err != nil {
		t.Fatalf("newOIDCValidator() error = %v, want nil", err)
	}
	if validator != nil {
		t.Fatal("newOIDCValidator() initialized a validator when OIDC is optional")
	}
	if got := discoveryCalls.Load(); got != 0 {
		t.Fatalf("OIDC discovery calls = %d, want 0", got)
	}
}

func TestNewOIDCValidatorInitializesForOAuth2ProxyTokens(t *testing.T) {
	var discoveryCalls atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveryCalls.Add(1)
		http.Error(w, "discovery reached", http.StatusInternalServerError)
	}))
	defer issuer.Close()

	validator, err := newOIDCValidator(config.Config{
		OAuth2ProxyAuthEnabled: true,
		OIDCIssuerURL:          issuer.URL,
		OIDCClientID:           "portal-client",
		OIDCAudience:           "ray-training-platform-api",
	})
	if err == nil || validator != nil {
		t.Fatalf("expected failed discovery to prove proxy verifier initialization, validator=%v err=%v", validator, err)
	}
	if got := discoveryCalls.Load(); got != 1 {
		t.Fatalf("OIDC discovery calls = %d, want 1", got)
	}
}
