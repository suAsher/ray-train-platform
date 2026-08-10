package auth

import (
	"context"
	"net/http"

	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

type stubLocalStore struct {
	record  LocalSessionRecord
	err     error
	touched int
}

func (s *stubLocalStore) FindLocalSessionByPublicID(context.Context, string) (LocalSessionRecord, error) {
	return s.record, s.err
}

func (s *stubLocalStore) TouchLocalSessionLastUsed(context.Context, string, time.Time) error {
	s.touched++
	return nil
}

func issuedTestSession(t *testing.T) domain.IssuedLocalSession {
	t.Helper()
	issued, err := domain.IssueLocalSession("sess-1", "user-1", "team-a", time.Hour, testAuthPepper(), time.Now())
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return issued
}

func testAuthPepper() []byte { return []byte(strings.Repeat("p", 32)) }

func localStoreFor(issued domain.IssuedLocalSession) *stubLocalStore {
	return &stubLocalStore{record: LocalSessionRecord{
		PublicID:  issued.PublicID,
		Digest:    issued.Digest,
		Principal: Principal{Subject: "user-1", Username: "alice", TenantID: "team-a", Roles: []string{"Engineer"}},
		ExpiresAt: time.Now().Add(time.Hour),
	}}
}

func TestLocalSessionAuthenticatorAcceptsValidToken(t *testing.T) {
	issued := issuedTestSession(t)
	authenticator, err := NewLocalSessionAuthenticator(localStoreFor(issued), testAuthPepper(), nil)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	principal, err := authenticator.Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if principal.AuthType != AuthTypeLocal || principal.TenantID != "team-a" || principal.Username != "alice" {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestLocalSessionAuthenticatorRejectsTamperedToken(t *testing.T) {
	issued := issuedTestSession(t)
	authenticator, _ := NewLocalSessionAuthenticator(localStoreFor(issued), testAuthPepper(), nil)
	forged := issued.Token[:len(issued.Token)-1] + "X"
	if _, err := authenticator.Authenticate(context.Background(), forged); err == nil {
		t.Fatalf("expected a tampered token to be rejected")
	}
}

func TestLocalSessionAuthenticatorRejectsExpiredRevokedAndDisabled(t *testing.T) {
	issued := issuedTestSession(t)
	revokedAt := time.Now().Add(-time.Minute)

	expired := localStoreFor(issued)
	expired.record.ExpiresAt = time.Now().Add(-time.Minute)

	revoked := localStoreFor(issued)
	revoked.record.RevokedAt = &revokedAt

	disabled := localStoreFor(issued)
	disabled.record.UserDisabled = true

	for name, store := range map[string]*stubLocalStore{"expired": expired, "revoked": revoked, "disabled": disabled} {
		authenticator, _ := NewLocalSessionAuthenticator(store, testAuthPepper(), nil)
		if _, err := authenticator.Authenticate(context.Background(), issued.Token); err == nil {
			t.Fatalf("expected %s session to be rejected", name)
		}
	}
}

func TestHybridMiddlewareRoutesLocalSessionTokens(t *testing.T) {
	issued := issuedTestSession(t)
	authenticator, _ := NewLocalSessionAuthenticator(localStoreFor(issued), testAuthPepper(), nil)

	response := serveMiddleware(t, HybridMiddlewareWithLocal(nil, nil, authenticator, true), "Bearer "+issued.Token)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected local session to authenticate, got %d body=%s", response.Code, response.Body.String())
	}
}

// A local session is an interactive human login, so it must reach the same
// endpoints an OIDC login can reach — otherwise the Portal is unusable
// without Keycloak.
func TestInteractiveSessionAllowsLocalAndOIDCButNotPAT(t *testing.T) {
	local := Principal{Subject: "u", TenantID: "t", AuthType: AuthTypeLocal, Roles: []string{"Engineer"}}
	oidc := Principal{Subject: "u", TenantID: "t", AuthType: AuthTypeOIDC, Roles: []string{"Engineer"}}
	pat := Principal{Subject: "u", TenantID: "t", AuthType: AuthTypePAT, Roles: []string{"Engineer"}}

	if response := servePrincipalMiddleware(t, local, RequireInteractiveSession(false)); response.Code != http.StatusNoContent {
		t.Fatalf("expected local session to pass, got %d", response.Code)
	}
	if response := servePrincipalMiddleware(t, oidc, RequireInteractiveSession(false)); response.Code != http.StatusNoContent {
		t.Fatalf("expected oidc session to pass, got %d", response.Code)
	}
	if response := servePrincipalMiddleware(t, pat, RequireInteractiveSession(false)); response.Code != http.StatusForbidden {
		t.Fatalf("expected PAT to be rejected, got %d", response.Code)
	}
}

func TestRequireScopesDoesNotConstrainLocalSessions(t *testing.T) {
	local := Principal{Subject: "u", TenantID: "t", AuthType: AuthTypeLocal, Roles: []string{"Engineer"}}
	if response := servePrincipalMiddleware(t, local, RequireScopes(domain.PATScopeJobsWrite)); response.Code != http.StatusNoContent {
		t.Fatalf("expected local session to bypass PAT scopes, got %d", response.Code)
	}
}
