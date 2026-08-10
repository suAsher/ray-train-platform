package domain

import (
	"strings"
	"testing"
	"time"
)

func TestWorkspaceAccessTokenRoundTrips(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	token, err := IssueWorkspaceAccessToken("ws-1", "user-1", testPepper(), now, 5*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := VerifyWorkspaceAccessToken(token, "ws-1", "user-1", testPepper(), now.Add(time.Minute)); err != nil {
		t.Fatalf("expected the token to verify: %v", err)
	}
}

// A token for one workspace must not open another, and it must not work for a
// different user even within the same tenant.
func TestWorkspaceAccessTokenIsBoundToWorkspaceAndUser(t *testing.T) {
	now := time.Now().UTC()
	token, err := IssueWorkspaceAccessToken("ws-1", "user-1", testPepper(), now, 5*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := VerifyWorkspaceAccessToken(token, "ws-2", "user-1", testPepper(), now); err == nil {
		t.Fatalf("a token must not open a different workspace")
	}
	if err := VerifyWorkspaceAccessToken(token, "ws-1", "user-2", testPepper(), now); err == nil {
		t.Fatalf("a token must not work for a different user")
	}
}

func TestWorkspaceAccessTokenExpires(t *testing.T) {
	now := time.Now().UTC()
	token, _ := IssueWorkspaceAccessToken("ws-1", "user-1", testPepper(), now, time.Minute)
	if err := VerifyWorkspaceAccessToken(token, "ws-1", "user-1", testPepper(), now.Add(2*time.Minute)); err == nil {
		t.Fatalf("expected an expired token to be rejected")
	}
}

func TestWorkspaceAccessTokenRejectsTamperingAndForeignSecret(t *testing.T) {
	now := time.Now().UTC()
	token, _ := IssueWorkspaceAccessToken("ws-1", "user-1", testPepper(), now, 5*time.Minute)

	tampered := token[:len(token)-1] + "0"
	if err := VerifyWorkspaceAccessToken(tampered, "ws-1", "user-1", testPepper(), now); err == nil {
		t.Fatalf("expected a tampered signature to be rejected")
	}
	otherPepper := []byte(strings.Repeat("x", 32))
	if err := VerifyWorkspaceAccessToken(token, "ws-1", "user-1", otherPepper, now); err == nil {
		t.Fatalf("a token signed with another secret must not verify")
	}
	for _, malformed := range []string{"", "nodot", "a.b.c.d"} {
		if err := VerifyWorkspaceAccessToken(malformed, "ws-1", "user-1", testPepper(), now); err == nil {
			t.Fatalf("expected %q to be rejected", malformed)
		}
	}
}
