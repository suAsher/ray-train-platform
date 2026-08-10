package httpapi

import "testing"

func TestRequestIDPreservesTrustedHeader(t *testing.T) {
	if got := RequestID("req-from-ingress"); got != "req-from-ingress" {
		t.Fatalf("expected existing request ID, got %q", got)
	}
}

func TestRequestIDGeneratesValueWhenHeaderIsMissing(t *testing.T) {
	first := RequestID("")
	second := RequestID("")
	if first == "" || second == "" || first == second {
		t.Fatalf("expected unique generated request IDs, got %q and %q", first, second)
	}
}
