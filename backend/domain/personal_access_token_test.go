package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testPATPepper() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func TestIssuePersonalAccessTokenUsesStrictFormatAndSafeMetadata(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	issued, err := IssuePersonalAccessToken(PersonalAccessTokenInput{ID: "pat-1", TenantID: "tenant-a", UserID: "user-a", Scopes: []string{PATScopeSourcesWrite, PATScopeJobsRead, PATScopeJobsRead}}, testPATPepper(), now)
	if err != nil {
		t.Fatalf("issue PAT: %v", err)
	}
	if len(issued.Token) != 64 || !strings.HasPrefix(issued.Token, "rpt_") || issued.Token[20] != '_' {
		t.Fatal("unexpected PAT format")
	}
	publicComponent := issued.Token[4:20]
	secretComponent := issued.Token[21:]
	publicBytes, err := base64.RawURLEncoding.DecodeString(publicComponent)
	if err != nil || len(publicBytes) != 12 {
		t.Fatalf("public id must encode 12 random bytes, length=%d err=%v", len(publicBytes), err)
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(secretComponent)
	if err != nil || len(secretBytes) != 32 {
		t.Fatalf("secret must encode 32 random bytes, length=%d err=%v", len(secretBytes), err)
	}
	if strings.Contains(publicComponent, "=") || strings.Contains(secretComponent, "=") {
		t.Fatal("PAT components must use raw URL base64 without padding")
	}
	if issued.PublicID != publicComponent || len(issued.Digest) != 64 {
		t.Fatalf("unexpected public id or digest: public=%q digestLen=%d", issued.PublicID, len(issued.Digest))
	}
	if !issued.ExpiresAt.Equal(now.Add(90 * 24 * time.Hour)) {
		t.Fatalf("expected default 90 day expiry, got %s", issued.ExpiresAt)
	}
	if got := strings.Join(issued.Scopes, ","); got != "jobs:read,sources:write" {
		t.Fatalf("expected sorted unique scopes, got %q", got)
	}
	encoded, err := json.Marshal(issued)
	if err != nil {
		t.Fatalf("marshal issued token: %v", err)
	}
	if strings.Contains(string(encoded), issued.Digest) {
		t.Fatal("digest leaked in issued token JSON")
	}
}

func TestIssuePersonalAccessTokenGeneratesIndependentSecrets(t *testing.T) {
	now := time.Now().UTC()
	first, err := IssuePersonalAccessToken(PersonalAccessTokenInput{ID: "pat-1", TenantID: "t", UserID: "u", Scopes: []string{PATScopeJobsRead}}, testPATPepper(), now)
	if err != nil {
		t.Fatalf("issue first PAT: %v", err)
	}
	second, err := IssuePersonalAccessToken(PersonalAccessTokenInput{ID: "pat-2", TenantID: "t", UserID: "u", Scopes: []string{PATScopeJobsRead}}, testPATPepper(), now)
	if err != nil {
		t.Fatalf("issue second PAT: %v", err)
	}
	if first.Token == second.Token || first.PublicID == second.PublicID || first.Digest == second.Digest {
		t.Fatal("independently issued PATs must not reuse public ids, secrets, or digests")
	}
}

func TestPersonalAccessTokenDigestIsStableAndRejectsWrongToken(t *testing.T) {
	issued, err := IssuePersonalAccessToken(PersonalAccessTokenInput{ID: "pat-1", TenantID: "t", UserID: "u", Scopes: []string{PATScopeJobsRead}}, testPATPepper(), time.Now().UTC())
	if err != nil {
		t.Fatalf("issue PAT: %v", err)
	}
	digest, err := DigestPersonalAccessToken(testPATPepper(), issued.Token)
	if err != nil {
		t.Fatalf("digest PAT: %v", err)
	}
	if digest != issued.Digest || !VerifyPersonalAccessToken(testPATPepper(), issued.Token, digest) {
		t.Fatal("expected stable HMAC digest and valid verification")
	}
	wrongSecret, err := base64.RawURLEncoding.DecodeString(issued.Token[21:])
	if err != nil || len(wrongSecret) != 32 {
		t.Fatalf("decode generated secret: length=%d err=%v", len(wrongSecret), err)
	}
	wrongSecret[0] ^= 0xff
	wrong := issued.Token[:21] + base64.RawURLEncoding.EncodeToString(wrongSecret)
	if _, err := ParsePATPublicID(wrong); err != nil {
		t.Fatalf("wrong secret must retain canonical PAT format: %v", err)
	}
	if VerifyPersonalAccessToken(testPATPepper(), wrong, digest) {
		t.Fatal("wrong token must not verify")
	}
}

func TestPersonalAccessTokenRejectsWeakPepperUnknownScopeAndExcessExpiry(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name   string
		input  PersonalAccessTokenInput
		pepper []byte
	}{
		{name: "weak pepper", input: PersonalAccessTokenInput{ID: "p", TenantID: "t", UserID: "u", Scopes: []string{PATScopeJobsRead}}, pepper: []byte("too-short")},
		{name: "unknown scope", input: PersonalAccessTokenInput{ID: "p", TenantID: "t", UserID: "u", Scopes: []string{"admin:all"}}, pepper: testPATPepper()},
		{name: "too long", input: PersonalAccessTokenInput{ID: "p", TenantID: "t", UserID: "u", Scopes: []string{PATScopeJobsWrite}, ExpiresAt: now.Add(366 * 24 * time.Hour)}, pepper: testPATPepper()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := IssuePersonalAccessToken(tc.input, tc.pepper, now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRedactPersonalAccessTokenKeepsOnlyPublicID(t *testing.T) {
	issued, err := IssuePersonalAccessToken(PersonalAccessTokenInput{ID: "pat-1", TenantID: "t", UserID: "u", Scopes: []string{PATScopeJobsRead}}, testPATPepper(), time.Now().UTC())
	if err != nil {
		t.Fatalf("issue PAT: %v", err)
	}
	if got := RedactPersonalAccessToken(issued.Token); got != issued.PublicID {
		t.Fatalf("redaction must keep only public id, got %q", got)
	}
	if got := RedactPersonalAccessToken("malformed"); got != "" {
		t.Fatalf("malformed token must redact to empty string, got %q", got)
	}
}
