package auth

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestPATAuthenticatorMarksPrincipalWithAuthenticationTypeAndScopes(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	issued := issuedAuthPAT(t, now)
	store := &fakePATStore{record: validPATRecord(issued)}
	authenticator, err := NewPATAuthenticator(store, authTestPepper(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	identity, err := authenticator.Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("authenticate PAT: %v", err)
	}
	if identity.Principal.AuthType != AuthTypePAT || !identity.Principal.HasScope(domain.PATScopeJobsRead) {
		t.Fatal("PAT principal must carry authentication type and granted scopes")
	}
}
