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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/gpu-allocations", nil)
	response := httptest.NewRecorder()
	adminRouter(handler, principal).ServeHTTP(response, request)
	return response
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
