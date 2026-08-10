package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	localSessionPrefix   = "rls_"
	defaultSessionExpiry = 12 * time.Hour
	maximumSessionExpiry = 30 * 24 * time.Hour
)

type LocalSession struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	TenantID  string     `json:"tenantId"`
	PublicID  string     `json:"publicId"`
	ExpiresAt time.Time  `json:"expiresAt"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type IssuedLocalSession struct {
	LocalSession
	Token  string `json:"token"`
	Digest string `json:"-"`
}

// IssueLocalSession mints an opaque session token using the same construction
// as personal access tokens: only the HMAC digest is persisted, so a database
// leak does not expose usable credentials.
func IssueLocalSession(id, userID, tenantID string, lifetime time.Duration, pepper []byte, now time.Time) (IssuedLocalSession, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(tenantID) == "" {
		return IssuedLocalSession{}, fmt.Errorf("session id, user, and tenant are required")
	}
	if err := validatePATPepper(pepper); err != nil {
		return IssuedLocalSession{}, err
	}
	if lifetime <= 0 {
		lifetime = defaultSessionExpiry
	}
	if lifetime > maximumSessionExpiry {
		return IssuedLocalSession{}, fmt.Errorf("session lifetime cannot exceed 30 days")
	}
	publicID, err := randomPATComponent(12)
	if err != nil {
		return IssuedLocalSession{}, fmt.Errorf("generate session public id: %w", err)
	}
	secret, err := randomPATComponent(32)
	if err != nil {
		return IssuedLocalSession{}, fmt.Errorf("generate session secret: %w", err)
	}
	plaintext := localSessionPrefix + publicID + "_" + secret
	digest, err := DigestPersonalAccessToken(pepper, plaintext)
	if err != nil {
		return IssuedLocalSession{}, err
	}
	now = now.UTC()
	return IssuedLocalSession{
		LocalSession: LocalSession{
			ID: id, UserID: userID, TenantID: tenantID, PublicID: publicID,
			ExpiresAt: now.Add(lifetime), CreatedAt: now,
		},
		Token: plaintext, Digest: digest,
	}, nil
}

func ParseLocalSessionPublicID(token string) (string, error) {
	const publicIDLength = 16
	const secretLength = 43
	if len(token) != len(localSessionPrefix)+publicIDLength+1+secretLength || !strings.HasPrefix(token, localSessionPrefix) {
		return "", fmt.Errorf("invalid session token format")
	}
	publicID := token[len(localSessionPrefix) : len(localSessionPrefix)+publicIDLength]
	separator := len(localSessionPrefix) + publicIDLength
	secret := token[separator+1:]
	if token[separator] != '_' || !validPATComponent(publicID, 12) || !validPATComponent(secret, 32) {
		return "", fmt.Errorf("invalid session token format")
	}
	return publicID, nil
}

func IsLocalSessionToken(token string) bool {
	return strings.HasPrefix(token, localSessionPrefix)
}
