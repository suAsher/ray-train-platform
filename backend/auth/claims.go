package auth

import (
	"fmt"
	"strings"
)

type AuthenticationType string

const (
	AuthTypeOIDC AuthenticationType = "oidc"
	AuthTypePAT  AuthenticationType = "pat"
	AuthTypeDemo AuthenticationType = "demo"
)

type RealmAccess struct {
	Roles []string `json:"roles"`
}

type TokenClaims struct {
	Subject           string      `json:"sub"`
	PreferredUsername string      `json:"preferred_username"`
	Email             string      `json:"email"`
	Groups            []string    `json:"groups"`
	RealmAccess       RealmAccess `json:"realm_access"`
}

type Principal struct {
	Subject  string
	Username string
	Email    string
	TenantID string
	Roles    []string
	AuthType AuthenticationType
	Scopes   []string
}

func (c TokenClaims) Principal(groupPrefix string) (Principal, error) {
	if strings.TrimSpace(c.Subject) == "" {
		return Principal{}, fmt.Errorf("token subject is required")
	}
	if groupPrefix == "" {
		groupPrefix = "platform/tenants/"
	}
	tenantID := tenantFromGroups(c.Groups, groupPrefix)
	if tenantID == "" {
		return Principal{}, fmt.Errorf("token does not contain a tenant group")
	}
	return Principal{
		Subject: c.Subject, Username: c.PreferredUsername, Email: c.Email, TenantID: tenantID,
		Roles: normalizedRoles(c.RealmAccess.Roles), AuthType: AuthTypeOIDC,
	}, nil
}

func tenantFromGroups(groups []string, prefix string) string {
	for _, group := range groups {
		if strings.HasPrefix(group, prefix) {
			if candidate := strings.Trim(strings.TrimPrefix(group, prefix), "/"); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func normalizedRoles(roles []string) []string {
	result := make([]string, 0, len(roles))
	for _, role := range roles {
		if role = strings.TrimSpace(role); role != "" {
			result = append(result, role)
		}
	}
	return result
}

func (p Principal) HasRole(role string) bool {
	for _, candidate := range p.Roles {
		if strings.EqualFold(candidate, role) {
			return true
		}
	}
	return false
}

func (p Principal) HasScope(scope string) bool {
	for _, candidate := range p.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

func (p Principal) Allowed(role string) bool {
	if p.HasRole("SuperAdmin") {
		return true
	}
	if p.HasRole("TenantAdmin") {
		return role == "TenantAdmin" || role == "Engineer"
	}
	return role == "Engineer" && p.HasRole("Engineer")
}

func DemoPrincipal() Principal {
	return Principal{
		Subject: "local-user", Username: "local-user", TenantID: "local",
		Roles: []string{"SuperAdmin"}, AuthType: AuthTypeDemo,
	}
}
