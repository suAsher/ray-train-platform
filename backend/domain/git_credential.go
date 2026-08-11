package domain

import (
	"fmt"
	"strings"
	"time"
)

// GitCredential points at the Kubernetes Secret holding a token for a private
// repository host. The token itself is never persisted in the database.
type GitCredential struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	Username   string    `json:"username,omitempty"`
	SecretName string    `json:"secretName"`
	CreatedBy  string    `json:"createdBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
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
	host := NormalizeGitHost(g.Host)
	if host == "" || strings.ContainsAny(host, " \t/\\") {
		return fmt.Errorf("host must be a bare hostname such as git.example.com")
	}
	if strings.TrimSpace(g.SecretName) == "" {
		return fmt.Errorf("secret name is required")
	}
	return nil
}

// GitCredentialSecretName derives a stable, DNS-safe Secret name so repeated
// registrations for one host reuse the same object.
func GitCredentialSecretName(tenantID, host string) string {
	return "git-cred-" + sanitizeDNSLabel(NormalizeGitHost(host))
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
