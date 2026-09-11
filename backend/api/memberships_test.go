package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type fakeMembershipStore struct {
	items      map[string][]domain.TenantMembership
	switched   string
	reassigned struct {
		identityID, expectedTenantID, targetTenantID string
		roles                                        []string
		deactivateOthers                             bool
	}
}

func (store *fakeMembershipStore) ListTenantMemberships(_ context.Context, identityID string) ([]domain.TenantMembership, error) {
	return append([]domain.TenantMembership(nil), store.items[identityID]...), nil
}
func (store *fakeMembershipStore) PutTenantMembership(_ context.Context, membership domain.TenantMembership) error {
	store.items[membership.IdentityID] = append(store.items[membership.IdentityID], membership)
	return nil
}
func (store *fakeMembershipStore) SetActiveTenant(_ context.Context, _, tenantID string) error {
	store.switched = tenantID
	return nil
}
func (store *fakeMembershipStore) SetTenantMembershipStatus(_ context.Context, _, _ string, _ domain.MembershipStatus) error {
	return nil
}
func (store *fakeMembershipStore) ReassignActiveMembership(_ context.Context, identityID, expectedTenantID, targetTenantID string, roles []string, deactivateOthers bool) error {
	store.reassigned.identityID = identityID
	store.reassigned.expectedTenantID = expectedTenantID
	store.reassigned.targetTenantID = targetTenantID
	store.reassigned.roles = append([]string(nil), roles...)
	store.reassigned.deactivateOthers = deactivateOthers
	store.items[identityID] = []domain.TenantMembership{{IdentityID: identityID, TenantID: targetTenantID, Roles: roles, Status: domain.MembershipStatusActive, Active: true}}
	return nil
}

func membershipRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal); c.Next() })
	v1 := router.Group("/api/v1")
	handler.RegisterSessionRoutes(v1)
	handler.RegisterAdminRoutes(v1)
	return router
}

func TestMembershipSelfListAndSwitchUseAuthenticatedIdentity(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{
		"user-a": {{IdentityID: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, Status: domain.MembershipStatusActive, Active: true}},
	}}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOAuth2Proxy}
	router := membershipRouter(handler, principal)

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/me/memberships", nil))
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"tenantId":"team-a"`)) {
		t.Fatalf("list response=%d %s", list.Code, list.Body.String())
	}

	switchResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/me/active-tenant", bytes.NewBufferString(`{"tenantId":"team-b"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(switchResponse, request)
	if switchResponse.Code != http.StatusOK || store.switched != "team-b" {
		t.Fatalf("switch response=%d %s switched=%q", switchResponse.Code, switchResponse.Body.String(), store.switched)
	}
}

func TestTenantAdminCannotGrantMembershipInAnotherTeam(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "admin-a", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeOAuth2Proxy}
	router := membershipRouter(handler, principal)
	body, _ := json.Marshal(putMembershipRequest{TenantID: "team-b", Roles: []string{domain.RoleEngineer}})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-b/memberships", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", response.Code, response.Body.String())
	}
}

func TestTenantAdminCannotGrantSuperAdminInOwnTeam(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "admin-a", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeOAuth2Proxy}
	router := membershipRouter(handler, principal)
	body, _ := json.Marshal(putMembershipRequest{TenantID: "team-a", Roles: []string{domain.RoleEngineer, domain.RoleSuperAdmin}})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-b/memberships", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", response.Code, response.Body.String())
	}
}

func TestSuperAdminAtomicallyReassignsUserTeam(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "root", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeOAuth2Proxy}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-b/active-membership", bytes.NewBufferString(`{"expectedTenantId":"local","targetTenantId":"devops","roles":["Engineer"]}`))
	request.Header.Set("Content-Type", "application/json")
	membershipRouter(handler, principal).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("reassign response=%d %s", response.Code, response.Body.String())
	}
	if store.reassigned.identityID != "user-b" || store.reassigned.expectedTenantID != "local" || store.reassigned.targetTenantID != "devops" || !store.reassigned.deactivateOthers {
		t.Fatalf("unexpected reassignment: %+v", store.reassigned)
	}
}

func TestTenantAdminCannotReassignUserTeam(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "lead", TenantID: "local", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeOAuth2Proxy}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-b/active-membership", bytes.NewBufferString(`{"expectedTenantId":"local","targetTenantId":"devops","roles":["Engineer"]}`))
	request.Header.Set("Content-Type", "application/json")
	membershipRouter(handler, principal).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || store.reassigned.identityID != "" {
		t.Fatalf("tenant admin reassignment response=%d writes=%+v", response.Code, store.reassigned)
	}
}
