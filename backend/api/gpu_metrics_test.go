package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type fakeGPUHistoryProvider struct {
	window         string
	node           string
	calls          int
	inventoryCalls int
	inventory      observability.GPUInventory
	history        observability.GPUHistory
}

func (provider *fakeGPUHistoryProvider) QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error) {
	return observability.JobMetrics{}, nil
}

func (provider *fakeGPUHistoryProvider) QueryGPUInventory(context.Context) (observability.GPUInventory, error) {
	provider.inventoryCalls++
	return provider.inventory, nil
}

func (provider *fakeGPUHistoryProvider) QueryGPUHistory(_ context.Context, window, node string) (observability.GPUHistory, error) {
	provider.calls++
	provider.window = window
	provider.node = node
	history := provider.history
	history.Window = window
	history.StepSeconds = 30
	if history.Devices == nil {
		history.Devices = []observability.GPUHistoryDevice{}
	}
	return history, nil
}

func TestGPUHistoryEndpointUsesAuthenticatedBoundedQuery(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{"SuperAdmin"}, AuthType: auth.AuthTypeLocal}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/gpu-metrics/history?window=1h&node=172.28.1.233", nil)
	response := httptest.NewRecorder()
	sessionRouter(handler, &principal).ServeHTTP(response, request)

	if response.Code != http.StatusOK || provider.calls != 1 || provider.window != "1h" || provider.node != "172.28.1.233" {
		t.Fatalf("unexpected history request: code=%d provider=%+v body=%s", response.Code, provider, response.Body.String())
	}
	var envelope struct {
		Data observability.GPUHistory `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.Window != "1h" {
		t.Fatalf("unexpected response: err=%v body=%s", err, response.Body.String())
	}
}

func TestGPUHistoryEndpointRejectsUnsupportedWindowBeforePrometheus(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{"SuperAdmin"}, AuthType: auth.AuthTypeLocal}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/gpu-metrics/history?window=30d", nil)
	response := httptest.NewRecorder()
	sessionRouter(handler, &principal).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || provider.calls != 0 {
		t.Fatalf("invalid window reached provider: code=%d calls=%d body=%s", response.Code, provider.calls, response.Body.String())
	}
}

func TestGPUHistoryEndpointRequiresAuthentication(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	response := httptest.NewRecorder()
	sessionRouter(handler, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/cluster/gpu-metrics/history?window=1h", nil))

	if response.Code != http.StatusUnauthorized || provider.calls != 0 {
		t.Fatalf("unauthenticated history request was not rejected: code=%d calls=%d", response.Code, provider.calls)
	}
}

func TestGPUMetricsEndpointsAllowOrdinaryInteractiveMembers(t *testing.T) {
	for _, authType := range []auth.AuthenticationType{auth.AuthTypeLocal, auth.AuthTypeOIDC, auth.AuthTypeOAuth2Proxy} {
		t.Run(string(authType), func(t *testing.T) {
			provider := &fakeGPUHistoryProvider{}
			handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
			principal := auth.Principal{Subject: "engineer", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: authType}
			for _, path := range []string{"/api/v1/cluster/gpu-metrics", "/api/v1/cluster/gpu-metrics/history?window=1h"} {
				response := httptest.NewRecorder()
				sessionRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusOK {
					t.Fatalf("member cannot read %s: code=%d body=%s", path, response.Code, response.Body.String())
				}
			}
			if provider.calls != 1 || provider.inventoryCalls != 1 {
				t.Fatalf("unexpected queries: history=%d inventory=%d", provider.calls, provider.inventoryCalls)
			}
		})
	}
}

func TestGPUHistoryEndpointRejectsUnsafeNodeBeforePrometheus(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/gpu-metrics/history?window=1h&node=worker%7D%5B5m%5D", nil)
	response := httptest.NewRecorder()
	sessionRouter(handler, &principal).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || provider.calls != 0 {
		t.Fatalf("unsafe node reached provider: code=%d calls=%d body=%s", response.Code, provider.calls, response.Body.String())
	}
}

func TestGPUMetricsEndpointsRejectPersonalAccessTokens(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "automation", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypePAT}

	for _, path := range []string{"/api/v1/cluster/gpu-metrics", "/api/v1/cluster/gpu-metrics/history?window=1h"} {
		response := httptest.NewRecorder()
		sessionRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("PAT accessed %s: code=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if provider.calls != 0 || provider.inventoryCalls != 0 {
		t.Fatalf("PAT requests reached metrics provider: history=%d inventory=%d", provider.calls, provider.inventoryCalls)
	}
}

func TestTenantAdministratorSeesOnlyOwnWorkloadAttribution(t *testing.T) {
	provider := &fakeGPUHistoryProvider{inventory: observability.GPUInventory{Devices: []observability.GPUDevice{
		{UUID: "own", Namespace: "tenant-team-a", PodName: "own-worker", ContainerName: "ray-worker"},
		{UUID: "other", Namespace: "tenant-team-b", PodName: "other-worker", ContainerName: "ray-worker"},
	}}}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "lead", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	sessionRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/cluster/gpu-metrics", nil))

	var envelope struct {
		Data observability.GPUInventory `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || len(envelope.Data.Devices) != 2 {
		t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
	}
	if envelope.Data.Devices[0].PodName != "own-worker" || envelope.Data.Devices[1].PodName != "" || envelope.Data.Devices[1].Namespace != "" {
		t.Fatalf("cross-tenant workload attribution leaked: %+v", envelope.Data.Devices)
	}
}

func TestGPUHistoryEndpointTrimsValidatedNode(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/gpu-metrics/history?window=1h&node=%20node-a%20", nil)
	response := httptest.NewRecorder()
	sessionRouter(handler, &principal).ServeHTTP(response, request)

	if response.Code != http.StatusOK || provider.node != "node-a" {
		t.Fatalf("validated node was not normalized: code=%d node=%q body=%s", response.Code, provider.node, response.Body.String())
	}
}

func TestGPUPoolMemberVisibilityRedactsOtherTeamAttribution(t *testing.T) {
	for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin} {
		t.Run(role, func(t *testing.T) {
			provider := &fakeGPUHistoryProvider{
				inventory: observability.GPUInventory{TotalGPUs: 3, Devices: []observability.GPUDevice{
					{UUID: "own", Namespace: "tenant-team-a", PodName: "own-worker", ContainerName: "ray-worker"},
					{UUID: "other", Namespace: "tenant-team-b", PodName: "other-worker", ContainerName: "ray-worker"},
					{UUID: "unknown", PodName: "unattributed-worker", ContainerName: "ray-worker"},
				}},
				history: observability.GPUHistory{Devices: []observability.GPUHistoryDevice{
					{UUID: "own", Namespace: "tenant-team-a", PodName: "own-worker", ContainerName: "ray-worker"},
					{UUID: "other", Namespace: "tenant-team-b", PodName: "other-worker", ContainerName: "ray-worker"},
					{UUID: "unknown", PodName: "unattributed-worker", ContainerName: "ray-worker"},
				}},
			}
			handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
			principal := auth.Principal{Subject: "member", TenantID: "team-a", Roles: []string{role}, AuthType: auth.AuthTypeOAuth2Proxy}
			for _, path := range []string{"/api/v1/cluster/gpu-metrics", "/api/v1/cluster/gpu-metrics/history?window=1h"} {
				response := httptest.NewRecorder()
				sessionRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				var envelope struct {
					Data struct {
						Devices []observability.GPUDevice `json:"devices"`
					} `json:"data"`
				}
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || len(envelope.Data.Devices) != 3 {
					t.Fatalf("unexpected pool response %s: code=%d body=%s", path, response.Code, response.Body.String())
				}
				for index, device := range envelope.Data.Devices {
					if index == 0 || role == domain.RoleSuperAdmin {
						if device.PodName == "" {
							t.Fatalf("permitted attribution removed: %+v", device)
						}
					} else if device.Namespace != "" || device.PodName != "" || device.ContainerName != "" {
						t.Fatalf("foreign or unattributed workload leaked: %+v", device)
					}
				}
			}
			if provider.inventory.Devices[1].PodName != "other-worker" || provider.history.Devices[1].PodName != "other-worker" {
				t.Fatal("response redaction mutated provider data")
			}
		})
	}
}

func TestGPUPoolReadDoesNotGrantAdministrativeOperations(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{})
	principal := auth.Principal{Subject: "engineer", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOAuth2Proxy}
	for _, operation := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/gpu-allocations"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/tenants/team-a/quota"},
		{http.MethodPut, "/api/v1/tenants/team-a/scheduling"},
		{http.MethodDelete, "/api/v1/admin/dev-workspaces/other-workspace"},
	} {
		response := httptest.NewRecorder()
		adminRouter(handler, principal).ServeHTTP(response, httptest.NewRequest(operation.method, operation.path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("pool reader gained %s %s: code=%d body=%s", operation.method, operation.path, response.Code, response.Body.String())
		}
	}
}
