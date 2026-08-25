package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type fakeJobGPUHistoryProvider struct {
	calls          int
	window         string
	namespace      string
	rayClusterName string
	history        observability.GPUHistory
	err            error
}

type metricsOnlyProvider struct{}

func (metricsOnlyProvider) QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error) {
	return observability.JobMetrics{}, nil
}

func (provider *fakeJobGPUHistoryProvider) QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error) {
	return observability.JobMetrics{}, nil
}

func (provider *fakeJobGPUHistoryProvider) QueryJobGPUHistory(_ context.Context, window, namespace, rayClusterName string) (observability.GPUHistory, error) {
	provider.calls++
	provider.window = window
	provider.namespace = namespace
	provider.rayClusterName = rayClusterName
	return provider.history, provider.err
}

func jobGPUHistoryRouter(handler *Handler, principal *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if principal != nil {
		identity := *principal
		router.Use(func(c *gin.Context) {
			c.Set("ray-platform-principal", identity)
			c.Next()
		})
	}
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func TestJobGPUHistoryOwnerUsesPersistedSelectorsAndDefaultWindow(t *testing.T) {
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{
		ID: "job-01", TenantID: "team-a", UserID: "user-a",
		KubernetesNS: "tenant-team-a", RayClusterName: "persisted-cluster",
	}}}
	provider := &fakeJobGPUHistoryProvider{history: observability.GPUHistory{
		Window: "1h", StepSeconds: 30, Devices: []observability.GPUHistoryDevice{},
	}}
	handler := NewHandler(repository, Options{Metrics: provider})
	principal := auth.Principal{
		Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC,
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/jobs/job-01/gpu-metrics?namespace=attacker&rayClusterName=forged",
		nil,
	)
	response := httptest.NewRecorder()

	jobGPUHistoryRouter(handler, &principal).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("owner request expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if provider.calls != 1 || provider.window != "1h" || provider.namespace != "tenant-team-a" || provider.rayClusterName != "persisted-cluster" {
		t.Fatalf("provider did not receive the persisted workload selectors: %+v", provider)
	}
	var envelope struct {
		Data observability.GPUHistory `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.Window != "1h" || len(envelope.Data.Devices) != 0 {
		t.Fatalf("successful empty history was not returned: err=%v body=%s", err, response.Body.String())
	}
}

func TestJobGPUHistoryAuthorizationBoundary(t *testing.T) {
	job := domain.TrainingJob{
		ID: "job-01", TenantID: "team-a", UserID: "owner-a",
		KubernetesNS: "tenant-team-a", RayClusterName: "cluster-a",
	}
	tests := []struct {
		name      string
		principal auth.Principal
		want      int
		wantCalls int
	}{
		{
			name: "owner Engineer",
			principal: auth.Principal{
				Subject: "owner-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal,
			},
			want: http.StatusOK, wantCalls: 1,
		},
		{
			name: "same-team other Engineer",
			principal: auth.Principal{
				Subject: "engineer-b", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal,
			},
			want: http.StatusNotFound,
		},
		{
			name: "same-team TenantAdmin",
			principal: auth.Principal{
				Subject: "admin-a", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeOIDC,
			},
			want: http.StatusOK, wantCalls: 1,
		},
		{
			name: "other-team Engineer",
			principal: auth.Principal{
				Subject: "owner-a", TenantID: "team-b", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC,
			},
			want: http.StatusNotFound,
		},
		{
			name: "other-team TenantAdmin",
			principal: auth.Principal{
				Subject: "admin-b", TenantID: "team-b", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
			},
			want: http.StatusNotFound,
		},
		{
			name: "SuperAdmin",
			principal: auth.Principal{
				Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal,
			},
			want: http.StatusOK, wantCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeJobGPUHistoryProvider{history: observability.GPUHistory{
				Window: "1h", Devices: []observability.GPUHistoryDevice{},
			}}
			handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Metrics: provider})
			response := httptest.NewRecorder()

			jobGPUHistoryRouter(handler, &test.principal).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/gpu-metrics?window=1h", nil),
			)

			if response.Code != test.want || provider.calls != test.wantCalls {
				t.Fatalf("expected status=%d calls=%d, got status=%d calls=%d body=%s", test.want, test.wantCalls, response.Code, provider.calls, response.Body.String())
			}
		})
	}
}

func TestJobGPUHistoryRejectsInvalidWindowBeforeProvider(t *testing.T) {
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{
		ID: "job-01", TenantID: "team-a", UserID: "user-a",
		KubernetesNS: "tenant-team-a", RayClusterName: "cluster-a",
	}}}
	provider := &fakeJobGPUHistoryProvider{}
	handler := NewHandler(repository, Options{Metrics: provider})
	principal := auth.Principal{
		Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal,
	}
	response := httptest.NewRecorder()

	jobGPUHistoryRouter(handler, &principal).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/gpu-metrics?window=30d", nil),
	)

	if response.Code != http.StatusBadRequest || provider.calls != 0 {
		t.Fatalf("invalid window reached provider: status=%d calls=%d body=%s", response.Code, provider.calls, response.Body.String())
	}
}

func TestJobGPUHistoryRequiresAuthenticationBeforeWindowValidation(t *testing.T) {
	provider := &fakeJobGPUHistoryProvider{}
	handler := NewHandler(&fakeJobRepository{}, Options{Metrics: provider})
	response := httptest.NewRecorder()

	jobGPUHistoryRouter(handler, nil).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/gpu-metrics?window=30d", nil),
	)

	if response.Code != http.StatusUnauthorized || provider.calls != 0 {
		t.Fatalf("unauthenticated request expected 401 before validation, got status=%d calls=%d body=%s", response.Code, provider.calls, response.Body.String())
	}
}

func TestJobGPUHistoryAcceptsSupportedWindows(t *testing.T) {
	for _, window := range []string{"15m", "1h", "6h", "24h", "7d"} {
		t.Run(window, func(t *testing.T) {
			provider := &fakeJobGPUHistoryProvider{history: observability.GPUHistory{
				Window: window, Devices: []observability.GPUHistoryDevice{},
			}}
			handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{
				ID: "job-01", TenantID: "team-a", UserID: "user-a",
				KubernetesNS: "tenant-team-a", RayClusterName: "cluster-a",
			}}}, Options{Metrics: provider})
			principal := auth.Principal{Subject: "user-a", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}
			response := httptest.NewRecorder()

			jobGPUHistoryRouter(handler, &principal).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/gpu-metrics?window="+window, nil),
			)

			if response.Code != http.StatusOK || provider.calls != 1 || provider.window != window {
				t.Fatalf("supported window failed: status=%d provider=%+v body=%s", response.Code, provider, response.Body.String())
			}
		})
	}
}

func TestJobGPUHistoryProviderFailures(t *testing.T) {
	job := domain.TrainingJob{
		ID: "job-01", TenantID: "team-a", UserID: "user-a",
		KubernetesNS: "tenant-team-a", RayClusterName: "cluster-a",
	}
	principal := auth.Principal{Subject: "user-a", TenantID: "team-a", AuthType: auth.AuthTypeLocal}
	tests := []struct {
		name     string
		metrics  MetricsProvider
		want     int
		wantCode string
	}{
		{name: "provider unavailable", metrics: metricsOnlyProvider{}, want: http.StatusServiceUnavailable, wantCode: "GPU_METRICS_UNAVAILABLE"},
		{name: "provider query error", metrics: &fakeJobGPUHistoryProvider{err: errors.New("prometheus unavailable")}, want: http.StatusBadGateway, wantCode: "GPU_HISTORY_QUERY_FAILED"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Metrics: test.metrics})
			response := httptest.NewRecorder()

			jobGPUHistoryRouter(handler, &principal).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/gpu-metrics?window=1h", nil),
			)

			if response.Code != test.want || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("expected status=%d code=%s, got status=%d body=%s", test.want, test.wantCode, response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "prometheus unavailable") {
				t.Fatalf("provider error leaked to response: %s", response.Body.String())
			}
		})
	}
}

func TestJobGPUHistoryPATScopeAndOwnership(t *testing.T) {
	job := domain.TrainingJob{
		ID: "job-01", TenantID: "team-a", UserID: "owner-a",
		KubernetesNS: "tenant-team-a", RayClusterName: "cluster-a",
	}
	tests := []struct {
		name      string
		principal auth.Principal
		want      int
		wantCalls int
	}{
		{
			name: "jobs-read owner PAT succeeds",
			principal: auth.Principal{
				Subject: "owner-a", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead},
			},
			want: http.StatusOK, wantCalls: 1,
		},
		{
			name: "jobs-write-only PAT is forbidden",
			principal: auth.Principal{
				Subject: "owner-a", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite},
			},
			want: http.StatusForbidden,
		},
		{
			name: "jobs-read other-user PAT is hidden",
			principal: auth.Principal{
				Subject: "user-b", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead},
			},
			want: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeJobGPUHistoryProvider{history: observability.GPUHistory{
				Window: "1h", Devices: []observability.GPUHistoryDevice{},
			}}
			handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Metrics: provider})
			response := httptest.NewRecorder()

			jobGPUHistoryRouter(handler, &test.principal).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/gpu-metrics?window=1h", nil),
			)

			if response.Code != test.want || provider.calls != test.wantCalls {
				t.Fatalf("expected status=%d calls=%d, got status=%d calls=%d body=%s", test.want, test.wantCalls, response.Code, provider.calls, response.Body.String())
			}
		})
	}
}
