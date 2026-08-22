package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

func seedRoleUser(store *fakeLocalAuthStore, username string, roles ...string) {
	store.users[username] = domain.LocalUser{
		ID: "user-" + username, Username: username, TenantID: "team-a", Roles: roles,
	}
}

func postRoles(router *gin.Engine, userID, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/local-users/"+userID+"/roles", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// A user created as an Engineer previously could never become a team
// administrator: account creation was the only place roles were ever set.
// Promotion is what grants write access to the shared team directory.
func TestSuperAdminPromotesAnEngineerToTenantAdmin(t *testing.T) {
	store := newFakeLocalAuthStore()
	seedRoleUser(store, "alice", domain.RoleEngineer)

	response := postRoles(userAdminRouter(store, superAdminPrincipal()), "user-alice", `{"roles":["TenantAdmin"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	updated := store.users["alice"]
	if len(updated.Roles) != 1 || updated.Roles[0] != domain.RoleTenantAdmin {
		t.Fatalf("expected the promoted role, got %v", updated.Roles)
	}
	// Team-shared write access is the point of the promotion.
	if !domain.CanManageDataSpace(domain.DataSpaceTeamShared, true, false) {
		t.Fatal("a tenant administrator must be able to publish to the shared team directory")
	}
}

// Roles are read from the user row on every request, so a demotion takes
// effect immediately; sessions are revoked so a downgraded operator cannot
// keep acting on an already-open page.
func TestDemotionPersistsAndRevokesExistingSessions(t *testing.T) {
	store := newFakeLocalAuthStore()
	seedRoleUser(store, "alice", domain.RoleTenantAdmin)

	if code := postRoles(userAdminRouter(store, superAdminPrincipal()), "user-alice", `{"roles":["Engineer"]}`).Code; code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	updated := store.users["alice"]
	if len(updated.Roles) != 1 || updated.Roles[0] != domain.RoleEngineer {
		t.Fatalf("expected the demotion to persist, got %v", updated.Roles)
	}
	if len(store.revokedAll) != 1 || store.revokedAll[0] != "user-alice" {
		t.Fatalf("a role change must revoke the user's sessions, got %v", store.revokedAll)
	}
}

// Cluster-wide privilege must not be grantable from a tenant-scoped screen.
func TestRoleChangeRefusesToGrantSuperAdmin(t *testing.T) {
	store := newFakeLocalAuthStore()
	seedRoleUser(store, "alice", domain.RoleEngineer)

	response := postRoles(userAdminRouter(store, superAdminPrincipal()), "user-alice", `{"roles":["SuperAdmin"]}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
	if roles := store.users["alice"].Roles; len(roles) != 1 || roles[0] != domain.RoleEngineer {
		t.Fatalf("the role must be unchanged, got %v", roles)
	}
}

// A team administrator promoting a peer would let any team lead mint more team
// leads without oversight, so granting the role stays with the super admin.
func TestTenantAdminCannotChangeRoles(t *testing.T) {
	store := newFakeLocalAuthStore()
	seedRoleUser(store, "alice", domain.RoleEngineer)

	response := postRoles(userAdminRouter(store, adminPrincipal()), "user-alice", `{"roles":["TenantAdmin"]}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
	if roles := store.users["alice"].Roles; len(roles) != 1 || roles[0] != domain.RoleEngineer {
		t.Fatalf("the role must be unchanged, got %v", roles)
	}
}

func TestRoleChangeRejectsAnEmptyOrUnknownRoleSet(t *testing.T) {
	store := newFakeLocalAuthStore()
	seedRoleUser(store, "alice", domain.RoleEngineer)
	router := userAdminRouter(store, superAdminPrincipal())

	for _, body := range []string{`{"roles":[]}`, `{"roles":["Wizard"]}`, `{}`} {
		if code := postRoles(router, "user-alice", body).Code; code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", body, code)
		}
	}
}
