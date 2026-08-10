package auth

import "testing"

func TestClaimsMapTenantAndRolesFromKeycloakGroups(t *testing.T) {
	claims := TokenClaims{
		Subject:           "kc-user-1",
		PreferredUsername: "engineer-01",
		Email:             "engineer@example.com",
		Groups:            []string{"platform/tenants/llm-team"},
		RealmAccess:       RealmAccess{Roles: []string{"Engineer"}},
	}
	principal, err := claims.Principal("platform/tenants/")
	if err != nil {
		t.Fatalf("map claims: %v", err)
	}
	if principal.Subject != "kc-user-1" || principal.TenantID != "llm-team" || !principal.HasRole("Engineer") {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if principal.AuthType != AuthTypeOIDC {
		t.Fatalf("OIDC claims must mark authentication type, got %q", principal.AuthType)
	}
}

func TestClaimsRejectMissingSubjectOrTenant(t *testing.T) {
	if _, err := (TokenClaims{}).Principal("platform/tenants/"); err == nil {
		t.Fatal("expected missing subject error")
	}
	claims := TokenClaims{Subject: "kc-user-1"}
	if _, err := claims.Principal("platform/tenants/"); err == nil {
		t.Fatal("expected missing tenant error")
	}
}

func TestPrincipalAuthorizeUsesRoleHierarchy(t *testing.T) {
	principal := Principal{Subject: "kc-admin", TenantID: "global", Roles: []string{"SuperAdmin"}}
	if !principal.Allowed("Engineer") || !principal.Allowed("TenantAdmin") || !principal.Allowed("SuperAdmin") {
		t.Fatal("super admin must inherit platform permissions")
	}
	engineer := Principal{Subject: "kc-engineer", TenantID: "llm-team", Roles: []string{"Engineer"}}
	if engineer.Allowed("TenantAdmin") {
		t.Fatal("engineer must not access tenant admin operations")
	}
}
