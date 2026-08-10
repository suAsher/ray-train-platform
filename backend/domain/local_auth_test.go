package domain

import (
	"strings"
	"testing"
	"time"
)

func testPepper() []byte { return []byte(strings.Repeat("p", 32)) }

func TestHashPasswordProducesVerifiableHash(t *testing.T) {
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "correct-horse" || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password must be bcrypt hashed, got %q", hash)
	}
	if !VerifyPassword(hash, "correct-horse") {
		t.Fatalf("expected the correct password to verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatalf("a wrong password must not verify")
	}
}

func TestHashPasswordRejectsShortPassword(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatalf("expected short passwords to be rejected")
	}
}

func TestVerifyPasswordRejectsEmptyHash(t *testing.T) {
	if VerifyPassword("", "anything") {
		t.Fatalf("an account without a password hash must never authenticate")
	}
}

func TestValidateUsernameRejectsUnsafeCharacters(t *testing.T) {
	for _, name := range []string{"", "has space", "kubectl;drop", "Ünicode", strings.Repeat("a", 65)} {
		if err := ValidateUsername(name); err == nil {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
	for _, name := range []string{"admin", "team.lead", "ml_eng-1", "ADMIN"} {
		if err := ValidateUsername(name); err != nil {
			t.Fatalf("expected %q to be accepted: %v", name, err)
		}
	}
}

func TestNormalizeUsernameIsCaseInsensitive(t *testing.T) {
	if NormalizeUsername("  ADMIN ") != "admin" {
		t.Fatalf("expected normalized username")
	}
}

func TestNormalizeRolesCanonicalizesAndRejectsUnknown(t *testing.T) {
	roles, err := NormalizeRoles([]string{"engineer", "SUPERADMIN", "Engineer"})
	if err != nil {
		t.Fatalf("normalize roles: %v", err)
	}
	if len(roles) != 2 || roles[0] != RoleEngineer || roles[1] != RoleSuperAdmin {
		t.Fatalf("unexpected roles: %v", roles)
	}
	if _, err := NormalizeRoles([]string{"root"}); err == nil {
		t.Fatalf("expected unknown role to be rejected")
	}
	if _, err := NormalizeRoles(nil); err == nil {
		t.Fatalf("expected empty roles to be rejected")
	}
}

func TestIssueLocalSessionProducesParsableOpaqueToken(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	issued, err := IssueLocalSession("sess-1", "user-1", "team-a", time.Hour, testPepper(), now)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if !IsLocalSessionToken(issued.Token) {
		t.Fatalf("token must carry the local session prefix: %s", issued.Token)
	}
	publicID, err := ParseLocalSessionPublicID(issued.Token)
	if err != nil || publicID != issued.PublicID {
		t.Fatalf("expected parsable public id, got %q err=%v", publicID, err)
	}
	if issued.Digest == "" || strings.Contains(issued.Digest, issued.Token) {
		t.Fatalf("digest must not embed the plaintext token")
	}
	if !VerifyPersonalAccessToken(testPepper(), issued.Token, issued.Digest) {
		t.Fatalf("expected digest to verify against the token")
	}
	if !issued.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected expiry: %s", issued.ExpiresAt)
	}
}

func TestParseLocalSessionRejectsForeignTokens(t *testing.T) {
	patLike := "rpt_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 43)
	for _, token := range []string{"", "rls_short", patLike, "rls_" + strings.Repeat("!", 60)} {
		if _, err := ParseLocalSessionPublicID(token); err == nil {
			t.Fatalf("expected %q to be rejected", token)
		}
	}
}

func TestIssueLocalSessionRejectsExcessiveLifetime(t *testing.T) {
	_, err := IssueLocalSession("s", "u", "t", 40*24*time.Hour, testPepper(), time.Now())
	if err == nil {
		t.Fatalf("expected overly long session lifetime to be rejected")
	}
}

func TestIssueLocalSessionRequiresStrongPepper(t *testing.T) {
	if _, err := IssueLocalSession("s", "u", "t", time.Hour, []byte("tooshort"), time.Now()); err == nil {
		t.Fatalf("expected weak pepper to be rejected")
	}
}
