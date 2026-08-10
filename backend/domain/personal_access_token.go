package domain

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	PATScopeJobsRead     = "jobs:read"
	PATScopeJobsWrite    = "jobs:write"
	PATScopeSourcesWrite = "sources:write"

	defaultPATLifetime = 90 * 24 * time.Hour
	maximumPATLifetime = 365 * 24 * time.Hour
	minimumPepperBytes = 32
)

var allowedPATScopes = map[string]struct{}{
	PATScopeJobsRead: {}, PATScopeJobsWrite: {}, PATScopeSourcesWrite: {},
}

type PersonalAccessToken struct {
	ID         string     `json:"id"`
	PublicID   string     `json:"publicId"`
	TenantID   string     `json:"tenantId"`
	UserID     string     `json:"userId"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type IssuedPersonalAccessToken struct {
	PersonalAccessToken
	Token  string `json:"token"`
	Digest string `json:"-"`
}

type PersonalAccessTokenInput struct {
	ID        string
	TenantID  string
	UserID    string
	Scopes    []string
	ExpiresAt time.Time
}

func IssuePersonalAccessToken(input PersonalAccessTokenInput, pepper []byte, now time.Time) (IssuedPersonalAccessToken, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.UserID) == "" {
		return IssuedPersonalAccessToken{}, fmt.Errorf("PAT id, tenant, and user are required")
	}
	if err := validatePATPepper(pepper); err != nil {
		return IssuedPersonalAccessToken{}, err
	}
	scopes, err := NormalizePATScopes(input.Scopes)
	if err != nil {
		return IssuedPersonalAccessToken{}, err
	}
	now = now.UTC()
	expiresAt := input.ExpiresAt.UTC()
	if input.ExpiresAt.IsZero() {
		expiresAt = now.Add(defaultPATLifetime)
	}
	if !expiresAt.After(now) {
		return IssuedPersonalAccessToken{}, fmt.Errorf("PAT expiry must be in the future")
	}
	if expiresAt.After(now.Add(maximumPATLifetime)) {
		return IssuedPersonalAccessToken{}, fmt.Errorf("PAT expiry cannot exceed 365 days")
	}
	publicID, err := randomPATComponent(12)
	if err != nil {
		return IssuedPersonalAccessToken{}, fmt.Errorf("generate PAT public id: %w", err)
	}
	secret, err := randomPATComponent(32)
	if err != nil {
		return IssuedPersonalAccessToken{}, fmt.Errorf("generate PAT secret: %w", err)
	}
	plaintext := "rpt_" + publicID + "_" + secret
	digest, err := DigestPersonalAccessToken(pepper, plaintext)
	if err != nil {
		return IssuedPersonalAccessToken{}, err
	}
	return IssuedPersonalAccessToken{
		PersonalAccessToken: PersonalAccessToken{ID: input.ID, PublicID: publicID, TenantID: input.TenantID, UserID: input.UserID, Scopes: scopes, ExpiresAt: expiresAt, CreatedAt: now},
		Token:               plaintext, Digest: digest,
	}, nil
}

func NormalizePATScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("at least one PAT scope is required")
	}
	unique := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if _, ok := allowedPATScopes[scope]; !ok {
			return nil, fmt.Errorf("unsupported PAT scope %q", scope)
		}
		unique[scope] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for scope := range unique {
		normalized = append(normalized, scope)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func ParsePATPublicID(token string) (string, error) {
	const publicIDLength = 16
	const secretLength = 43
	if len(token) != len("rpt_")+publicIDLength+1+secretLength || !strings.HasPrefix(token, "rpt_") {
		return "", fmt.Errorf("invalid PAT format")
	}
	publicID := token[len("rpt_") : len("rpt_")+publicIDLength]
	separator := len("rpt_") + publicIDLength
	secret := token[separator+1:]
	if token[separator] != '_' || !validPATComponent(publicID, 12) || !validPATComponent(secret, 32) {
		return "", fmt.Errorf("invalid PAT format")
	}
	return publicID, nil
}

func DigestPersonalAccessToken(pepper []byte, token string) (string, error) {
	if err := validatePATPepper(pepper); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func VerifyPersonalAccessToken(pepper []byte, token, expectedDigest string) bool {
	actualHex, err := DigestPersonalAccessToken(pepper, token)
	if err != nil {
		return false
	}
	actual, actualErr := hex.DecodeString(actualHex)
	expected, expectedErr := hex.DecodeString(expectedDigest)
	if actualErr != nil || expectedErr != nil || len(expected) != sha256.Size {
		return false
	}
	return hmac.Equal(actual, expected)
}

func RedactPersonalAccessToken(token string) string {
	publicID, err := ParsePATPublicID(token)
	if err != nil {
		return ""
	}
	return publicID
}

func validatePATPepper(pepper []byte) error {
	if len(pepper) < minimumPepperBytes {
		return fmt.Errorf("PAT pepper must contain at least 32 bytes")
	}
	return nil
}

func randomPATComponent(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validPATComponent(value string, byteLength int) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == byteLength && base64.RawURLEncoding.EncodeToString(decoded) == value
}
