package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type fakeMembershipStore struct {
	items      map[string][]domain.TenantMembership
	switched   string
	reassigned struct {
		identityID, expectedTenantID, targetTenantID string
		roles                                        []string
		deactivateOthers                             bool
	}
	audits      []repositories.AdministrativeAuditEvent
	reassignErr error
	putCalls    int
	statusCalls int
}

func (store *fakeMembershipStore) CreateAdministrativeAuditLog(_ context.Context, event repositories.AdministrativeAuditEvent) error {
	store.audits = append(store.audits, event)
	return nil
}

func (store *fakeMembershipStore) ListTenantMemberships(_ context.Context, identityID string) ([]domain.TenantMembership, error) {
	return append([]domain.TenantMembership(nil), store.items[identityID]...), nil
}
func (store *fakeMembershipStore) PutTenantMembership(_ context.Context, membership domain.TenantMembership) error {
	store.putCalls++
	store.items[membership.IdentityID] = append(store.items[membership.IdentityID], membership)
	return nil
}
func (store *fakeMembershipStore) SetActiveTenant(_ context.Context, _, tenantID string) error {
	store.switched = tenantID
	return nil
}
func (store *fakeMembershipStore) SetTenantMembershipStatus(_ context.Context, _, _ string, _ domain.MembershipStatus) error {
	store.statusCalls++
	return nil
}
func (store *fakeMembershipStore) ReassignActiveMembership(_ context.Context, identityID, expectedTenantID, targetTenantID string, roles []string, deactivateOthers bool) error {
	if store.reassignErr != nil {
		return store.reassignErr
	}
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
	if response.Code != http.StatusForbidden || store.putCalls != 0 {
		t.Fatalf("expected 403 without mutation, got %d %s", response.Code, response.Body.String())
	}
}

func TestMembershipWritesRequireSuperAdmin(t *testing.T) {
	for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin} {
		for _, target := range []string{"lead", "engineer", "other-admin", "other-team-user", "root"} {
			for _, operation := range []struct {
				name, method, suffix, body string
			}{
				{"add engineer", http.MethodPut, "/memberships", `{"tenantId":"team-a","roles":["Engineer"]}`},
				{"promote admin", http.MethodPut, "/memberships", `{"tenantId":"team-a","roles":["TenantAdmin"]}`},
				{"disable membership", http.MethodPatch, "/memberships/team-a", `{"status":"INACTIVE"}`},
				{"restore membership", http.MethodPatch, "/memberships/team-a", `{"status":"ACTIVE"}`},
			} {
				t.Run(role+"/"+target+"/"+operation.name, func(t *testing.T) {
					store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
					handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
					principal := auth.Principal{Subject: "lead", TenantID: "team-a", Roles: []string{role}, AuthType: auth.AuthTypeOAuth2Proxy}
					request := httptest.NewRequest(operation.method, "/api/v1/users/"+target+operation.suffix, bytes.NewBufferString(operation.body))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					membershipRouter(handler, principal).ServeHTTP(response, request)
					wantStatus, wantWrites := http.StatusForbidden, 0
					if role == domain.RoleSuperAdmin {
						wantStatus, wantWrites = http.StatusOK, 1
					}
					if response.Code != wantStatus || store.putCalls+store.statusCalls != wantWrites {
						t.Fatalf("status=%d writes=%d body=%s", response.Code, store.putCalls+store.statusCalls, response.Body.String())
					}
				})
			}
		}
	}
}

func TestSuperAdminMembershipCannotGrantGlobalRole(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "root", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-b/memberships", bytes.NewBufferString(`{"tenantId":"team-a","roles":["SuperAdmin"]}`))
	request.Header.Set("Content-Type", "application/json")
	membershipRouter(handler, principal).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.putCalls != 0 {
		t.Fatalf("global role grant: status=%d writes=%d", response.Code, store.putCalls)
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
	if len(store.audits) != 1 || store.audits[0].Action != "tenant_membership.reassigned" || store.audits[0].ResourceID != "user-b" || store.audits[0].SourceTenantID != "local" || store.audits[0].TargetTenantID != "devops" {
		t.Fatalf("unexpected audit events: %+v", store.audits)
	}
}

func TestMembershipReassignmentDoesNotExposeStoreErrors(t *testing.T) {
	store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}, reassignErr: errors.New("pq: secret internal database detail")}
	handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
	principal := auth.Principal{Subject: "root", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeOAuth2Proxy}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-b/active-membership", bytes.NewBufferString(`{"expectedTenantId":"local","targetTenantId":"devops","roles":["Engineer"]}`))
	request.Header.Set("Content-Type", "application/json")
	membershipRouter(handler, principal).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || bytes.Contains(response.Body.Bytes(), []byte("secret internal database detail")) {
		t.Fatalf("unexpected response=%d %s", response.Code, response.Body.String())
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

func TestSuperAdminCanReassignOwnTeamWithoutGrantingGlobalRole(t *testing.T) {
	for _, test := range []struct {
		roles  string
		status int
	}{
		{`["Engineer"]`, http.StatusOK},
		{`["SuperAdmin"]`, http.StatusBadRequest},
	} {
		store := &fakeMembershipStore{items: map[string][]domain.TenantMembership{}}
		handler := NewHandler(&fakeJobRepository{}, Options{Memberships: store})
		principal := auth.Principal{Subject: "root", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeOAuth2Proxy}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/users/root/active-membership", bytes.NewBufferString(`{"expectedTenantId":"local","targetTenantId":"devops","roles":`+test.roles+`}`))
		request.Header.Set("Content-Type", "application/json")
		membershipRouter(handler, principal).ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("self team assignment: %d %s", response.Code, response.Body.String())
		}
		if test.status == http.StatusOK && store.reassigned.identityID != "root" {
			t.Fatalf("wrong target: %+v", store.reassigned)
		}
		if test.status != http.StatusOK && store.reassigned.identityID != "" {
			t.Fatal("team membership granted global role")
		}
	}
}
