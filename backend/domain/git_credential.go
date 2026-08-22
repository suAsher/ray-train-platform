package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type GitCredentialScope string

const (
	// GitCredentialScopePersonal is private to one platform account and takes
	// precedence for that account's private repositories.
	GitCredentialScopePersonal GitCredentialScope = "personal"
	// GitCredentialScopeTeam is administered by a tenant administrator and is
	// the fallback for repositories shared by the team.
	GitCredentialScopeTeam GitCredentialScope = "team"
)

// GitCredential points at the Kubernetes Secret holding a token for a private
// repository host. The token itself is never persisted in the database.
type GitCredential struct {
	ID          string             `json:"id"`
	TenantID    string             `json:"tenantId"`
	Scope       GitCredentialScope `json:"scope"`
	OwnerUserID string             `json:"-"`
	Name        string             `json:"name"`
	Host        string             `json:"host"`
	Username    string             `json:"username,omitempty"`
	SecretName  string             `json:"-"`
	CreatedBy   string             `json:"createdBy,omitempty"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

func NormalizeGitHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if index := strings.Index(host, "/"); index >= 0 {
		host = host[:index]
	}
	return host
}

func (g GitCredential) Validate() error {
	if strings.TrimSpace(g.TenantID) == "" {
		return fmt.Errorf("tenant is required")
	}
	if strings.TrimSpace(g.Name) == "" {
		return fmt.Errorf("credential name is required")
	}
	if g.Scope != GitCredentialScopePersonal && g.Scope != GitCredentialScopeTeam {
		return fmt.Errorf("credential scope must be personal or team")
	}
	if g.Scope == GitCredentialScopePersonal && strings.TrimSpace(g.OwnerUserID) == "" {
		return fmt.Errorf("personal credential owner is required")
	}
	if g.Scope == GitCredentialScopeTeam && strings.TrimSpace(g.OwnerUserID) != "" {
		return fmt.Errorf("team credential must not have a personal owner")
	}
	host := NormalizeGitHost(g.Host)
	if host == "" || strings.ContainsAny(host, " \t/\\") {
		return fmt.Errorf("host must be a bare hostname such as git.example.com")
	}
	if strings.TrimSpace(g.SecretName) == "" {
		return fmt.Errorf("secret name is required")
	}
	return nil
}

// GitCredentialSecretName derives a stable, DNS-safe Secret name. Personal
// credentials include a non-reversible owner hash so two users can register
// distinct credentials for the same Git host without overwriting one another.
func GitCredentialSecretName(tenantID string, scope GitCredentialScope, ownerUserID, host string) string {
	name := "git-cred-" + sanitizeDNSLabel(NormalizeGitHost(host))
	if scope != GitCredentialScopePersonal {
		return truncateDNSLabel(name+"-team", 63)
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(tenantID) + ":" + strings.TrimSpace(ownerUserID)))
	return truncateDNSLabel(name+"-user-"+hex.EncodeToString(digest[:])[:10], 63)
}

func truncateDNSLabel(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.TrimRight(value[:limit], "-")
}

func sanitizeDNSLabel(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
			continue
		}
		builder.WriteByte('-')
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		result = "default"
	}
	if len(result) > 40 {
		result = strings.Trim(result[:40], "-")
	}
	return result
}
