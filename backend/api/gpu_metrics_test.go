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
	return observability.GPUHistory{Window: window, StepSeconds: 30, Devices: []observability.GPUHistoryDevice{}}, nil
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

func TestGPUMetricsEndpointsRequireAdministratorRole(t *testing.T) {
	provider := &fakeGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	principal := auth.Principal{Subject: "engineer", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}

	for _, path := range []string{
		"/api/v1/cluster/gpu-metrics",
		"/api/v1/cluster/gpu-metrics/history?window=1h",
	} {
		response := httptest.NewRecorder()
		sessionRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("engineer accessed %s: code=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if provider.calls != 0 || provider.inventoryCalls != 0 {
		t.Fatalf("forbidden requests reached metrics provider: history=%d inventory=%d", provider.calls, provider.inventoryCalls)
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
