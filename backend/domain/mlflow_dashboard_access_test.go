package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMLflowDashboardSessionBindsTenantSubjectAndNonce(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), now.Add(time.Minute)); err != nil {
		t.Fatalf("verify session: %v", err)
	}

	for name, expected := range map[string][2]string{
		"altered tenant":  {"tenant-b", "user-a"},
		"altered subject": {"tenant-a", "user-b"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyMLflowDashboardSession(token, expected[0], expected[1], testPepper(), now); err == nil {
				t.Fatal("claim mismatch accepted")
			}
		})
	}

	payload, signature, found := strings.Cut(token, ".")
	if !found {
		t.Fatalf("issued token has no separator: %q", token)
	}
	decodedPayload, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	alteredPayload := strings.Replace(string(decodedPayload), "\x00nonce-a\x00", "\x00nonce-b\x00", 1)
	if alteredPayload == string(decodedPayload) {
		t.Fatal("test could not locate nonce in payload")
	}
	tamperedNonceToken := base64.RawURLEncoding.EncodeToString([]byte(alteredPayload)) + "." + signature
	if err := VerifyMLflowDashboardSession(tamperedNonceToken, "tenant-a", "user-a", testPepper(), now); err == nil {
		t.Fatal("altered nonce accepted")
	}

	alteredSignature := "0" + signature[1:]
	if alteredSignature == signature {
		alteredSignature = "1" + signature[1:]
	}
	if err := VerifyMLflowDashboardSession(payload+"."+alteredSignature, "tenant-a", "user-a", testPepper(), now); err == nil {
		t.Fatal("altered signature accepted")
	}
}

func TestMLflowDashboardSessionUsesURLSafePayloadAndDefaultTTL(t *testing.T) {
	if MLflowDashboardTicketTTL != 2*time.Minute {
		t.Fatalf("ticket TTL = %v", MLflowDashboardTicketTTL)
	}
	if MLflowDashboardSessionTTL != 8*time.Hour {
		t.Fatalf("session TTL = %v", MLflowDashboardSessionTTL)
	}

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, ttl := range []time.Duration{0, -time.Second} {
		token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, ttl)
		if err != nil {
			t.Fatalf("issue session with ttl %v: %v", ttl, err)
		}
		payload, _, found := strings.Cut(token, ".")
		if !found {
			t.Fatalf("issued token has no separator: %q", token)
		}
		if strings.ContainsAny(payload, "+/=") {
			t.Fatalf("payload is not raw URL-safe base64: %q", payload)
		}
		if _, err := base64.RawURLEncoding.DecodeString(payload); err != nil {
			t.Fatalf("payload is not raw URL-safe base64: %v", err)
		}
		if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), now.Add(MLflowDashboardSessionTTL)); err != nil {
			t.Fatalf("default-TTL session expired too early: %v", err)
		}
		if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), now.Add(MLflowDashboardSessionTTL+time.Second)); err == nil {
			t.Fatal("default-TTL session did not expire")
		}
	}
}

func TestMLflowDashboardSessionRejectsMalformedTokensWithoutLeakingThem(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	payload, signature, found := strings.Cut(token, ".")
	if !found {
		t.Fatalf("issued token has no separator: %q", token)
	}

	malformedTokens := []string{
		"",
		payload,
		"." + signature,
		payload + ".",
		payload + "." + signature + ".extra",
		"not+url-safe." + signature,
		payload + ".not-hex",
	}
	for _, malformed := range malformedTokens {
		err := VerifyMLflowDashboardSession(malformed, "tenant-a", "user-a", testPepper(), now)
		if err == nil {
			t.Fatalf("malformed token accepted: %q", malformed)
		}
		if malformed != "" && strings.Contains(err.Error(), malformed) {
			t.Fatalf("error exposed token contents: %v", err)
		}
	}
}

func TestMLflowDashboardSessionRejectsOversizedSignatureWithoutDecodingAllocation(t *testing.T) {
	const oversizedSignatureLength = sha256.Size*2 + (4 << 20)

	token := "payload." + strings.Repeat("a", oversizedSignatureLength)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), time.Now().UTC())
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("oversized signature accepted")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated >= uint64(oversizedSignatureLength/4) {
		t.Fatalf("oversized signature allocated %d bytes before rejection", allocated)
	}
}

func TestMLflowDashboardSessionExpires(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, time.Second)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	err = VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), now.Add(2*time.Second))
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry error, got %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("expiry error exposed token contents: %v", err)
	}
}

func TestMLflowDashboardSessionRejectsBlankValuesAndWeakKeys(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for name, values := range map[string][3]string{
		"blank tenant":  {" ", "user-a", "nonce-a"},
		"blank subject": {"tenant-a", "\t", "nonce-a"},
		"blank nonce":   {"tenant-a", "user-a", "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := IssueMLflowDashboardSession(values[0], values[1], values[2], testPepper(), now, time.Hour); err == nil {
				t.Fatal("blank value accepted")
			}
		})
	}
	if _, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", []byte("short"), now, time.Hour); err == nil {
		t.Fatal("weak issue key accepted")
	}

	token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	for name, expected := range map[string][2]string{
		"blank expected tenant":  {" ", "user-a"},
		"blank expected subject": {"tenant-a", "\t"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyMLflowDashboardSession(token, expected[0], expected[1], testPepper(), now); err == nil {
				t.Fatal("blank expected value accepted")
			}
		})
	}
	if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", []byte("short"), now); err == nil {
		t.Fatal("weak verification key accepted")
	}
}

func TestMLflowDashboardSessionRejectsNULDelimitedClaims(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for name, values := range map[string][3]string{
		"tenant":  {"tenant-a\x00tenant-b", "user-a", "nonce-a"},
		"subject": {"tenant-a", "user-a\x00user-b", "nonce-a"},
		"nonce":   {"tenant-a", "user-a", "nonce-a\x00nonce-b"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := IssueMLflowDashboardSession(values[0], values[1], values[2], testPepper(), now, time.Hour); err == nil {
				t.Fatal("NUL-delimited claim accepted")
			}
		})
	}
}
