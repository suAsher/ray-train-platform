package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

type fakePATStore struct {
	record    PATRecord
	findErr   error
	touchErr  error
	publicID  string
	touchTime time.Time
	touches   int
}

func (s *fakePATStore) FindPATByPublicID(_ context.Context, publicID string) (PATRecord, error) {
	s.publicID = publicID
	return s.record, s.findErr
}

func (s *fakePATStore) TouchPATLastUsed(_ context.Context, publicID string, usedAt time.Time) error {
	s.publicID = publicID
	s.touchTime = usedAt
	s.touches++
	return s.touchErr
}

func authTestPepper() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func issuedAuthPAT(t *testing.T, now time.Time) domain.IssuedPersonalAccessToken {
	t.Helper()
	issued, err := domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{
		ID: "pat-1", TenantID: "tenant-a", UserID: "user-a",
		Scopes: []string{domain.PATScopeJobsRead, domain.PATScopeJobsWrite},
	}, authTestPepper(), now)
	if err != nil {
		t.Fatalf("issue PAT: %v", err)
	}
	return issued
}

func validPATRecord(issued domain.IssuedPersonalAccessToken) PATRecord {
	return PATRecord{
		PublicID: issued.PublicID, Digest: issued.Digest,
		Principal: Principal{Subject: "user-a", Username: "engineer", Email: "engineer@example.com", TenantID: "tenant-a", Roles: []string{"Engineer"}},
		Scopes:    issued.Scopes, ExpiresAt: issued.ExpiresAt,
	}
}

func TestPATAuthenticatorRestoresPrincipalScopesAndTouchesUsage(t *testing.T) {
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
	if identity.Principal.Subject != "user-a" || identity.Principal.TenantID != "tenant-a" || !identity.HasScope(domain.PATScopeJobsWrite) {
		t.Fatalf("unexpected PAT identity subject=%q tenant=%q hasWrite=%t", identity.Principal.Subject, identity.Principal.TenantID, identity.HasScope(domain.PATScopeJobsWrite))
	}
	if store.publicID != issued.PublicID || store.touches != 1 || !store.touchTime.Equal(now) {
		t.Fatalf("unexpected usage touch public=%q touches=%d time=%s", store.publicID, store.touches, store.touchTime)
	}
}

func TestPATAuthenticatorRejectsWrongExpiredAndRevokedTokens(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	issued := issuedAuthPAT(t, now)
	revokedAt := now.Add(-time.Minute)
	wrongSecret, err := base64.RawURLEncoding.DecodeString(issued.Token[21:])
	if err != nil || len(wrongSecret) != 32 {
		t.Fatalf("decode generated secret: length=%d err=%v", len(wrongSecret), err)
	}
	wrongSecret[0] ^= 0xff
	wrongToken := issued.Token[:21] + base64.RawURLEncoding.EncodeToString(wrongSecret)
	if _, err := domain.ParsePATPublicID(wrongToken); err != nil {
		t.Fatalf("wrong secret must retain canonical PAT format: %v", err)
	}
	cases := []struct {
		name   string
		token  string
		record PATRecord
	}{
		{name: "wrong secret", token: wrongToken, record: PATRecord{PublicID: issued.PublicID, Digest: issued.Digest, ExpiresAt: issued.ExpiresAt}},
		{name: "expired", token: issued.Token, record: PATRecord{PublicID: issued.PublicID, Digest: issued.Digest, ExpiresAt: now}},
		{name: "revoked", token: issued.Token, record: PATRecord{PublicID: issued.PublicID, Digest: issued.Digest, ExpiresAt: issued.ExpiresAt, RevokedAt: &revokedAt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakePATStore{record: tc.record}
			authenticator, err := NewPATAuthenticator(store, authTestPepper(), func() time.Time { return now })
			if err != nil {
				t.Fatalf("new authenticator: %v", err)
			}
			if _, err := authenticator.Authenticate(context.Background(), tc.token); !errors.Is(err, ErrInvalidPAT) {
				t.Fatalf("expected generic invalid PAT error, got %v", err)
			}
			if store.touches != 0 {
				t.Fatal("invalid PAT must not update last_used_at")
			}
		})
	}
}

func TestPATAuthenticatorClassifiesLookupErrors(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	issued := issuedAuthPAT(t, now)
	authenticatorFor := func(store PATStore) *PATAuthenticator {
		t.Helper()
		authenticator, err := NewPATAuthenticator(store, authTestPepper(), func() time.Time { return now })
		if err != nil {
			t.Fatalf("new authenticator: %v", err)
		}
		return authenticator
	}

	t.Run("not found is invalid credentials", func(t *testing.T) {
		store := &fakePATStore{findErr: ErrPATNotFound}
		_, err := authenticatorFor(store).Authenticate(context.Background(), issued.Token)
		if !errors.Is(err, ErrInvalidPAT) {
			t.Fatalf("expected invalid PAT for missing record, got %v", err)
		}
	})

	t.Run("store failure remains an internal error", func(t *testing.T) {
		storeFailure := errors.New("PAT database unavailable")
		store := &fakePATStore{findErr: storeFailure}
		_, err := authenticatorFor(store).Authenticate(context.Background(), issued.Token)
		if !errors.Is(err, storeFailure) || errors.Is(err, ErrInvalidPAT) {
			t.Fatalf("expected wrapped store failure distinct from invalid PAT, got %v", err)
		}
		if store.touches != 0 {
			t.Fatal("lookup failure must not touch last_used_at")
		}
	})
}

func TestPATAuthenticatorClassifiesTouchFailureAsInternal(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	issued := issuedAuthPAT(t, now)
	touchFailure := errors.New("PAT usage update unavailable")
	store := &fakePATStore{record: validPATRecord(issued), touchErr: touchFailure}
	authenticator, err := NewPATAuthenticator(store, authTestPepper(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	_, err = authenticator.Authenticate(context.Background(), issued.Token)
	if !errors.Is(err, touchFailure) || errors.Is(err, ErrInvalidPAT) {
		t.Fatalf("expected wrapped touch failure distinct from invalid PAT, got %v", err)
	}
	if store.touches != 1 {
		t.Fatalf("expected one failed touch attempt, got %d", store.touches)
	}
}

func TestPATAuthenticatorRejectsWeakPepperAndMissingStore(t *testing.T) {
	if _, err := NewPATAuthenticator(nil, authTestPepper(), time.Now); err == nil {
		t.Fatal("expected nil store error")
	}
	if _, err := NewPATAuthenticator(&fakePATStore{}, []byte("short"), time.Now); err == nil {
		t.Fatal("expected weak pepper error")
	}
}

func TestPATIdentityHasOnlyAllowedNormalizedScopes(t *testing.T) {
	identity := PATIdentity{Scopes: []string{domain.PATScopeSourcesWrite, domain.PATScopeJobsRead}}
	if !identity.HasScope(domain.PATScopeJobsRead) || identity.HasScope("admin:all") {
		t.Fatalf("unexpected scope evaluation: %+v", identity.Scopes)
	}
}
