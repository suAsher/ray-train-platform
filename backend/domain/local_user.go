package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// minimumLocalPasswordLength is deliberately modest: local accounts exist so
	// the platform can be operated without an external IdP, and the deployment
	// is expected to sit behind a private network.
	minimumLocalPasswordLength = 8
	maximumLocalPasswordLength = 128
	maximumLocalUsernameLength = 64
)

var platformRoles = map[string]string{
	"superadmin":  RoleSuperAdmin,
	"tenantadmin": RoleTenantAdmin,
	"engineer":    RoleEngineer,
}

const (
	RoleSuperAdmin  = "SuperAdmin"
	RoleTenantAdmin = "TenantAdmin"
	RoleEngineer    = "Engineer"
)

type LocalUser struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"`
	TenantID     string    `json:"tenantId"`
	Roles        []string  `json:"roles"`
	Disabled     bool      `json:"disabled"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// NormalizeUsername lower-cases and trims so that lookups are stable and a
// username cannot be duplicated by changing its case.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func ValidateUsername(username string) error {
	normalized := NormalizeUsername(username)
	if normalized == "" {
		return fmt.Errorf("username is required")
	}
	if len(normalized) > maximumLocalUsernameLength {
		return fmt.Errorf("username must be at most %d characters", maximumLocalUsernameLength)
	}
	for _, char := range normalized {
		isLower := char >= 'a' && char <= 'z'
		isDigit := char >= '0' && char <= '9'
		if !isLower && !isDigit && char != '-' && char != '_' && char != '.' {
			return fmt.Errorf("username may only contain letters, digits, '-', '_' and '.'")
		}
	}
	return nil
}

func ValidatePassword(password string) error {
	if len(password) < minimumLocalPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minimumLocalPasswordLength)
	}
	if len(password) > maximumLocalPasswordLength {
		return fmt.Errorf("password must be at most %d characters", maximumLocalPasswordLength)
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether the password matches. bcrypt comparison is
// constant time for a given hash, so a wrong password cannot be distinguished
// from a right one by timing.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NormalizeRoles maps role names case-insensitively onto the three platform
// roles and rejects anything else, so a typo cannot silently create an account
// with no effective permissions.
func NormalizeRoles(roles []string) ([]string, error) {
	if len(roles) == 0 {
		return nil, fmt.Errorf("at least one role is required")
	}
	unique := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		canonical, ok := platformRoles[strings.ToLower(strings.TrimSpace(role))]
		if !ok {
			return nil, fmt.Errorf("unsupported role %q", role)
		}
		unique[canonical] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for role := range unique {
		normalized = append(normalized, role)
	}
	sort.Strings(normalized)
	return normalized, nil
}
