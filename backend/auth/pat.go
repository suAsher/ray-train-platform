package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ray-train-platform-backend/domain"
)

var (
	ErrInvalidPAT  = errors.New("invalid personal access token")
	ErrPATNotFound = errors.New("personal access token not found")
)

type PATRecord struct {
	PublicID   string
	Digest     string `json:"-"`
	Principal  Principal
	Scopes     []string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

type PATStore interface {
	FindPATByPublicID(ctx context.Context, publicID string) (PATRecord, error)
	TouchPATLastUsed(ctx context.Context, publicID string, usedAt time.Time) error
}

type PATIdentity struct {
	Principal Principal
	Scopes    []string
}

func (i PATIdentity) HasScope(scope string) bool {
	for _, candidate := range i.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

type PATAuthenticator struct {
	store  PATStore
	pepper []byte
	now    func() time.Time
}

func NewPATAuthenticator(store PATStore, pepper []byte, now func() time.Time) (*PATAuthenticator, error) {
	if store == nil {
		return nil, fmt.Errorf("PAT store is required")
	}
	if len(pepper) < 32 {
		return nil, fmt.Errorf("PAT pepper must contain at least 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	pepperCopy := append([]byte(nil), pepper...)
	return &PATAuthenticator{store: store, pepper: pepperCopy, now: now}, nil
}

func (a *PATAuthenticator) Authenticate(ctx context.Context, rawToken string) (PATIdentity, error) {
	if a == nil || a.store == nil {
		return PATIdentity{}, ErrInvalidPAT
	}
	publicID, err := domain.ParsePATPublicID(rawToken)
	if err != nil {
		return PATIdentity{}, ErrInvalidPAT
	}
	record, err := a.store.FindPATByPublicID(ctx, publicID)
	if err != nil {
		if errors.Is(err, ErrPATNotFound) {
			return PATIdentity{}, ErrInvalidPAT
		}
		return PATIdentity{}, fmt.Errorf("find PAT authentication record: %w", err)
	}
	if record.PublicID != publicID {
		return PATIdentity{}, ErrInvalidPAT
	}
	now := a.now().UTC()
	if record.RevokedAt != nil || !record.ExpiresAt.After(now) {
		return PATIdentity{}, ErrInvalidPAT
	}
	if !domain.VerifyPersonalAccessToken(a.pepper, rawToken, record.Digest) {
		return PATIdentity{}, ErrInvalidPAT
	}
	scopes, err := domain.NormalizePATScopes(record.Scopes)
	if err != nil || record.Principal.Subject == "" || record.Principal.TenantID == "" {
		return PATIdentity{}, ErrInvalidPAT
	}
	if err := a.store.TouchPATLastUsed(ctx, publicID, now); err != nil {
		return PATIdentity{}, fmt.Errorf("update PAT last-used timestamp: %w", err)
	}
	principal := record.Principal
	principal.AuthType = AuthTypePAT
	principal.Scopes = append([]string(nil), scopes...)
	return PATIdentity{Principal: principal, Scopes: append([]string(nil), scopes...)}, nil
}
