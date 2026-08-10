package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ray-train-platform-backend/domain"
)

const AuthTypeLocal AuthenticationType = "local"

var (
	ErrInvalidLocalSession  = errors.New("invalid local session")
	ErrLocalSessionNotFound = errors.New("local session not found")
)

type LocalSessionRecord struct {
	PublicID     string
	Digest       string `json:"-"`
	Principal    Principal
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	LastUsedAt   *time.Time
	UserDisabled bool
}

type LocalSessionStore interface {
	FindLocalSessionByPublicID(ctx context.Context, publicID string) (LocalSessionRecord, error)
	TouchLocalSessionLastUsed(ctx context.Context, publicID string, usedAt time.Time) error
}

type LocalSessionVerifier interface {
	Authenticate(ctx context.Context, rawToken string) (Principal, error)
}

type LocalSessionAuthenticator struct {
	store  LocalSessionStore
	pepper []byte
	now    func() time.Time
}

func NewLocalSessionAuthenticator(store LocalSessionStore, pepper []byte, now func() time.Time) (*LocalSessionAuthenticator, error) {
	if store == nil {
		return nil, fmt.Errorf("local session store is required")
	}
	if len(pepper) < 32 {
		return nil, fmt.Errorf("local session pepper must contain at least 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	return &LocalSessionAuthenticator{store: store, pepper: append([]byte(nil), pepper...), now: now}, nil
}

func (a *LocalSessionAuthenticator) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if a == nil || a.store == nil {
		return Principal{}, ErrInvalidLocalSession
	}
	publicID, err := domain.ParseLocalSessionPublicID(rawToken)
	if err != nil {
		return Principal{}, ErrInvalidLocalSession
	}
	record, err := a.store.FindLocalSessionByPublicID(ctx, publicID)
	if err != nil {
		if errors.Is(err, ErrLocalSessionNotFound) {
			return Principal{}, ErrInvalidLocalSession
		}
		return Principal{}, fmt.Errorf("find local session: %w", err)
	}
	now := a.now().UTC()
	// A disabled account must lose access immediately, without waiting for the
	// already-issued session token to expire.
	if record.UserDisabled || record.RevokedAt != nil || !record.ExpiresAt.After(now) {
		return Principal{}, ErrInvalidLocalSession
	}
	if !domain.VerifyPersonalAccessToken(a.pepper, rawToken, record.Digest) {
		return Principal{}, ErrInvalidLocalSession
	}
	if record.Principal.Subject == "" || record.Principal.TenantID == "" {
		return Principal{}, ErrInvalidLocalSession
	}
	if err := a.store.TouchLocalSessionLastUsed(ctx, publicID, now); err != nil {
		return Principal{}, fmt.Errorf("update local session last-used timestamp: %w", err)
	}
	principal := clonePrincipal(record.Principal)
	principal.AuthType = AuthTypeLocal
	principal.Scopes = nil
	return principal, nil
}
