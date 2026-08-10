package auth

import (
	"net/http"
	"strings"
	"testing"
)

func TestHybridMiddlewareTreatsTypedNilPATVerifierAsInvalidCredentials(t *testing.T) {
	var disabled *PATAuthenticator
	canonicalPAT := "rpt_AAAAAAAAAAAAAAAA_" + strings.Repeat("A", 43)
	response := serveMiddleware(t, HybridMiddleware(nil, disabled, true), "Bearer "+canonicalPAT)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled PAT authentication expected 401, got %d: %s", response.Code, response.Body.String())
	}
}
