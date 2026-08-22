package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type fakeAdminStore struct {
	tenants   []repositories.TenantSummary
	users     []repositories.UserSummary
	quotaSets []struct {
		tenantID string
		limit    int
	}
	setQuotaErr error
}

func (store *fakeAdminStore) ListTenantSummaries(context.Context) ([]repositories.TenantSummary, error) {
	return store.tenants, nil
}

func (store *fakeAdminStore) ListUserSummaries(context.Context) ([]repositories.UserSummary, error) {
	return store.users, nil
}

func (store *fakeAdminStore) CreateTenant(context.Context, domain.Tenant) error { return nil }

func (store *fakeAdminStore) SetTenantGPUQuota(_ context.Context, tenantID string, limit int) error {
	if store.setQuotaErr != nil {
		return store.setQuotaErr
	}
	store.quotaSets = append(store.quotaSets, struct {
		tenantID string
		limit    int
	}{tenantID, limit})
	for index, tenant := range store.tenants {
		if tenant.ID == tenantID {
			store.tenants[index].GPUQuotaLimit = limit
		}
	}
	return nil
}

func adminRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterAdminRoutes(router.Group("/api/v1"))
	return router
}

func postQuota(t *testing.T, handler *Handler, principal auth.Principal, tenantID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID+"/quota", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	adminRouter(handler, principal).ServeHTTP(response, request)
	return response
}

// The admin console offered a "修改配额" button that only printed a message
// telling the operator to edit Helm values. The tenant GPU limit is enforced at
// submission time from the database, so it is editable here.
func TestSuperAdminChangesTheEnforcedTenantGPUQuota(t *testing.T) {
	store := &fakeAdminStore{tenants: []repositories.TenantSummary{{ID: "team-a", GPUQuotaLimit: 8}}}
	handler := NewHandler(&fakeJobRepository{}, Options{Admin: store})
	principal := auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}}

	response := postQuota(t, handler, principal, "team-a", `{"gpuQuota":16}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if len(store.quotaSets) != 1 || store.quotaSets[0].tenantID != "team-a" || store.quotaSets[0].limit != 16 {
		t.Fatalf("expected the quota to be persisted, got %+v", store.quotaSets)
	}
	var envelope struct {
		Data repositories.TenantSummary `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.GPUQuotaLimit != 16 {
		t.Fatalf("expected the updated tenant to be returned, got %+v", envelope.Data)
	}
}

// Capacity is shared across the cluster, so reallocating it is not something a
// single team's administrator may do for itself.
func TestTenantAdminCannotChangeItsOwnGPUQuota(t *testing.T) {
	store := &fakeAdminStore{tenants: []repositories.TenantSummary{{ID: "team-a", GPUQuotaLimit: 8}}}
	handler := NewHandler(&fakeJobRepository{}, Options{Admin: store})
	principal := auth.Principal{Subject: "lead", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}}

	response := postQuota(t, handler, principal, "team-a", `{"gpuQuota":64}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
	if len(store.quotaSets) != 0 {
		t.Fatalf("a tenant admin must not reallocate cluster capacity, got %+v", store.quotaSets)
	}
}

func TestTenantQuotaRejectsValuesOutsideTheAllowedRange(t *testing.T) {
	store := &fakeAdminStore{tenants: []repositories.TenantSummary{{ID: "team-a", GPUQuotaLimit: 8}}}
	handler := NewHandler(&fakeJobRepository{}, Options{Admin: store})
	principal := auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}}

	for _, body := range []string{`{"gpuQuota":-1}`, `{"gpuQuota":100000}`} {
		response := postQuota(t, handler, principal, "team-a", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", body, response.Code)
		}
	}
	if len(store.quotaSets) != 0 {
		t.Fatalf("no invalid quota may reach the store, got %+v", store.quotaSets)
	}
}

func TestTenantQuotaReportsAnUnknownTenant(t *testing.T) {
	store := &fakeAdminStore{tenants: []repositories.TenantSummary{{ID: "team-a", GPUQuotaLimit: 8}}}
	handler := NewHandler(&fakeJobRepository{}, Options{Admin: store})
	principal := auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}}

	response := postQuota(t, handler, principal, "team-missing", `{"gpuQuota":8}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}
