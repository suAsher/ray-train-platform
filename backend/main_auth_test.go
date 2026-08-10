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
