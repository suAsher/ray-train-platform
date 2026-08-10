package api

import (
	"net/http"
	"testing"
	"time"
)

func TestPersonalAccessTokenResponsesDisableCaching(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	principal := oidcPATPrincipal()
	router := newPATAPIRouter(t, &fakePATManagementStore{}, &principal, false, now)
	response := performPATRequest(router, http.MethodPost, "/api/v1/personal-access-tokens", `{}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected create success, got %d", response.Code)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("PAT response must disable caching, got %q", response.Header().Get("Cache-Control"))
	}
}
