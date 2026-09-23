package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type fakeGPUAllocationStore struct {
	items      []domain.GPUAllocation
	tenantID   string
	allTenants bool
	calls      int
}

func (store *fakeGPUAllocationStore) ListGPUAllocations(_ context.Context, tenantID string, allTenants bool) ([]domain.GPUAllocation, error) {
	store.calls++
	store.tenantID = tenantID
	store.allTenants = allTenants
	return append([]domain.GPUAllocation(nil), store.items...), nil
}

func getGPUAllocations(handler *Handler, principal auth.Principal) *httptest.ResponseRecorder {
	return getGPUAllocationsWithQuery(handler, principal, "")
}

func getGPUAllocationsWithQuery(handler *Handler, principal auth.Principal, query string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/gpu-allocations"+query, nil)
	response := httptest.NewRecorder()
	adminRouter(handler, principal).ServeHTTP(response, request)
	return response
}

func TestGPUAllocationsExplicitScope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		role       string
		query      string
		status     int
		allTenants bool
	}{
		{"team admin global read", domain.RoleTenantAdmin, "?scope=all", http.StatusOK, true},
		{"team admin own team", domain.RoleTenantAdmin, "?scope=team", http.StatusOK, false},
		{"super admin global read", domain.RoleSuperAdmin, "?scope=all", http.StatusOK, true},
		{"super admin own team", domain.RoleSuperAdmin, "?scope=team", http.StatusOK, false},
		{"engineer cannot request global read", domain.RoleEngineer, "?scope=all", http.StatusForbidden, false},
		{"role missing", "", "?scope=all", http.StatusForbidden, false},
		{"unknown scope", domain.RoleTenantAdmin, "?scope=other", http.StatusBadRequest, false},
		{"duplicate scope", domain.RoleTenantAdmin, "?scope=team&scope=all", http.StatusBadRequest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeGPUAllocationStore{items: []domain.GPUAllocation{{ID: "foreign-job", TenantID: "team-b"}}}
			handler := NewHandler(&fakeJobRepository{}, Options{GPUAllocations: store})
			response := getGPUAllocationsWithQuery(handler, auth.Principal{Subject: "lead", TenantID: "team-a", Roles: []string{tc.role}}, tc.query)
			if response.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, response.Code, response.Body.String())
			}
			if tc.status != http.StatusOK {
				if store.calls != 0 {
					t.Fatal("rejected request reached allocation store")
				}
				return
			}
			wantTenant := "team-a"
			if tc.allTenants {
				wantTenant = ""
			}
			if store.calls != 1 || store.allTenants != tc.allTenants || store.tenantID != wantTenant {
				t.Fatalf("incorrect scope: calls=%d tenant=%q all=%v", store.calls, store.tenantID, store.allTenants)
			}
		})
	}
}

func TestGPUAllocationsLetsSuperAdminSeeAllTenants(t *testing.T) {
	store := &fakeGPUAllocationStore{items: []domain.GPUAllocation{{ID: "workspace-a", TenantID: "team-a"}}}
	handler := NewHandler(&fakeJobRepository{}, Options{GPUAllocations: store})
	response := getGPUAllocations(handler, auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}})

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if store.calls != 1 || !store.allTenants || store.tenantID != "" {
		t.Fatalf("expected all-tenant query, got calls=%d tenant=%q all=%v", store.calls, store.tenantID, store.allTenants)
	}
	var envelope struct {
		Data []domain.GPUAllocation `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope.Data) != 1 {
		t.Fatalf("unexpected response: err=%v body=%s", err, response.Body.String())
	}
}

func TestGPUAllocationsScopesTenantAdminToOwnTeam(t *testing.T) {
	store := &fakeGPUAllocationStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{GPUAllocations: store})
	response := getGPUAllocations(handler, auth.Principal{Subject: "lead", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}})

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if store.calls != 1 || store.allTenants || store.tenantID != "team-a" {
		t.Fatalf("expected own-team query, got calls=%d tenant=%q all=%v", store.calls, store.tenantID, store.allTenants)
	}
}

func TestGPUAllocationsRejectsEngineer(t *testing.T) {
	store := &fakeGPUAllocationStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{GPUAllocations: store})
	response := getGPUAllocations(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized request reached store %d times", store.calls)
	}
}
