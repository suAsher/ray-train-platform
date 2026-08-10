package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func userAdminRouter(store LocalAuthStore, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	localAuthHandler(store).RegisterUserAdminRoutes(router.Group("/api/v1"))
	return router
}

func adminPrincipal() auth.Principal {
	return auth.Principal{Subject: "admin-1", Username: "admin", TenantID: "team-a",
		Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal}
}

func engineerPrincipal() auth.Principal {
	return auth.Principal{Subject: "eng-1", Username: "bob", TenantID: "team-a",
		Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
}

func postUser(router *gin.Engine, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/local-users", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestAdminCreatesEngineerAccountInOwnTenant(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"]}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", response.Code, response.Body.String())
	}
	created := store.users["alice"]
	if created.TenantID != "team-a" {
		t.Fatalf("new account must land in the admin's tenant, got %+v", created)
	}
	if len(created.Roles) != 1 || created.Roles[0] != domain.RoleEngineer {
		t.Fatalf("unexpected roles: %+v", created.Roles)
	}
	if !domain.VerifyPassword(created.PasswordHash, "engineer-pass") {
		t.Fatalf("password was not stored as a usable bcrypt hash")
	}
	// The response must never echo the password back.
	if strings.Contains(response.Body.String(), "engineer-pass") {
		t.Fatalf("response leaked the password: %s", response.Body.String())
	}
}

func TestEngineerCannotCreateAccounts(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, engineerPrincipal()),
		`{"username":"mallory","password":"engineer-pass","roles":["SuperAdmin"]}`)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
	if len(store.users) != 0 {
		t.Fatalf("no account may be created by a non-admin")
	}
}

// A tenant admin must not be able to mint an account outside their own tenant.
func TestTenantAdminCannotCreateAccountInAnotherTenant(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"],"tenantId":"team-b"}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", response.Code, response.Body.String())
	}
	if store.users["alice"].TenantID != "team-a" {
		t.Fatalf("tenant from the request body must be ignored, got %q", store.users["alice"].TenantID)
	}
}

func TestCreateUserRejectsWeakPasswordAndBadRole(t *testing.T) {
	store := newFakeLocalAuthStore()
	router := userAdminRouter(store, adminPrincipal())

	if response := postUser(router, `{"username":"alice","password":"short","roles":["Engineer"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("expected weak password rejection, got %d", response.Code)
	}
	if response := postUser(router, `{"username":"alice","password":"engineer-pass","roles":["root"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown role rejection, got %d", response.Code)
	}
}

func TestListUsersReturnsOnlyOwnTenantForTenantAdmin(t *testing.T) {
	store := newFakeLocalAuthStore()
	store.users["alice"] = domain.LocalUser{ID: "u1", Username: "alice", TenantID: "team-a", Roles: []string{domain.RoleEngineer}}
	store.users["carol"] = domain.LocalUser{ID: "u2", Username: "carol", TenantID: "team-b", Roles: []string{domain.RoleEngineer}}

	router := userAdminRouter(store, adminPrincipal())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/local-users", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var envelope struct {
		Data []domain.LocalUser `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].Username != "alice" {
		t.Fatalf("tenant admin must only see their own tenant, got %+v", envelope.Data)
	}
	if envelope.Data[0].PasswordHash != "" {
		t.Fatalf("password hash must never be serialized")
	}
}
