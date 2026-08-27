package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/runtimecatalog"
)

type fakePlatformQuotaStore struct {
	quota     domain.TenantQuota
	err       error
	tenantIDs []string
}

type platformTenantQuotaPayload struct {
	GPULimit     int `json:"gpuLimit"`
	GPUUsed      int `json:"gpuUsed"`
	GPUAvailable int `json:"gpuAvailable"`
}

func (store *fakePlatformQuotaStore) TenantGPUQuota(_ context.Context, tenantID string) (domain.TenantQuota, error) {
	store.tenantIDs = append(store.tenantIDs, tenantID)
	return store.quota, store.err
}

func decodePlatformLimits(t *testing.T, body []byte) platformLimitsResponse {
	t.Helper()
	var envelope struct {
		Success bool                   `json:"success"`
		Data    platformLimitsResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.Success {
		t.Fatalf("expected successful envelope, got %s", string(body))
	}
	return envelope.Data
}

func decodePlatformTenantQuota(t *testing.T, body []byte) *platformTenantQuotaPayload {
	t.Helper()
	var envelope struct {
		Data struct {
			TenantQuota *platformTenantQuotaPayload `json:"tenantQuota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode tenant quota: %v", err)
	}
	return envelope.Data.TenantQuota
}

func limitsRouter(handler *Handler, principal *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if principal != nil {
			c.Set("ray-platform-principal", *principal)
		}
		c.Next()
	})
	handler.RegisterSessionRoutes(router.Group("/api/v1"))
	return router
}

func TestPlatformLimitsReportTheDeploymentCeilingsTheServerEnforces(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "local", GPULimit: 16, GPUAvailable: 16}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "local", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	limits := decodePlatformLimits(t, response.Body.Bytes())
	if limits.MaxWorkerReplicas != 2 || limits.MaxGPUsPerWorker != 8 || limits.MaxTotalGPUs != 16 {
		t.Fatalf("expected the configured ceilings, got %+v", limits)
	}
}

func TestPlatformLimitsExposeEffectiveRuntimeCapabilities(t *testing.T) {
	principal := auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}

	t.Run("feature flags disabled", func(t *testing.T) {
		handler := NewHandler(&fakeJobRepository{}, Options{})
		response := httptest.NewRecorder()
		limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

		runtime := decodePlatformLimits(t, response.Body.Bytes()).Runtime
		if runtime.ManagedEnabled || runtime.CanaryEnabled {
			t.Fatalf("disabled features advertised as enabled: %+v", runtime)
		}
		if strings.Join(runtime.AvailableEngines, ",") != string(domain.TrainingEngineRayDDP) {
			t.Fatalf("unexpected disabled engine list: %+v", runtime)
		}
		if runtime.ProductionRayVersion != domain.RayVersionProduction || runtime.CanaryRayVersion != domain.RayVersionCanary {
			t.Fatalf("unexpected runtime versions: %+v", runtime)
		}
	})

	t.Run("feature flags enabled", func(t *testing.T) {
		handler := NewHandler(&fakeJobRepository{}, Options{RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true, CanaryEnabled: true}})
		response := httptest.NewRecorder()
		limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

		limits := decodePlatformLimits(t, response.Body.Bytes())
		if !limits.Runtime.ManagedEnabled || !limits.Runtime.CanaryEnabled || strings.Join(limits.Runtime.AvailableEngines, ",") != "ray-ddp,ray-train" {
			t.Fatalf("enabled capabilities missing: %+v", limits.Runtime)
		}
		limits.Runtime.AvailableEngines[0] = "mutated"
		second := httptest.NewRecorder()
		limitsRouter(handler, &principal).ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))
		if got := decodePlatformLimits(t, second.Body.Bytes()).Runtime.AvailableEngines[0]; got != string(domain.TrainingEngineRayDDP) {
			t.Fatalf("runtime capabilities retained mutable response state: %q", got)
		}
	})
}

func TestPlatformLimitsExposeEnabledRuntimeCachePolicyDefensively(t *testing.T) {
	allowed := []string{"200Gi", "500Gi", "1Ti", "2Ti", "4Ti", "5Ti"}
	handler := NewHandler(&fakeJobRepository{}, Options{LocalCache: LocalCachePolicy{
		Enabled: true, AllowedSizes: allowed, DefaultSize: "200Gi", MaxSize: "5Ti", MountPath: "/mnt/cache", MountPaths: []string{"/mnt/cache", "/mnt/cache2"},
	}})
	allowed[0] = "1Ti"
	principal := auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	cache := decodePlatformLimits(t, response.Body.Bytes()).Cache
	if !cache.Enabled || cache.DefaultMode != string(domain.CacheModeOff) || strings.Join(cache.Modes, ",") != "off,runtime" {
		t.Fatalf("unexpected cache modes: %#v", cache)
	}
	if strings.Join(cache.AllowedSizes, ",") != "200Gi,500Gi,1Ti,2Ti,4Ti,5Ti" || cache.DefaultSize != "200Gi" || cache.MaxSize != "5Ti" || cache.MountPath != "/mnt/cache" || strings.Join(cache.MountPaths, ",") != "/mnt/cache,/mnt/cache2" {
		t.Fatalf("unexpected cache policy: %#v", cache)
	}
	cache.AllowedSizes[0] = "mutated"
	second := httptest.NewRecorder()
	limitsRouter(handler, &principal).ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))
	if got := decodePlatformLimits(t, second.Body.Bytes()).Cache.AllowedSizes[0]; got != "200Gi" {
		t.Fatalf("response exposed mutable cache policy: %q", got)
	}
}

func TestPlatformLimitsExposeOnlyOffWhenRuntimeCacheDisabled(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{LocalCache: LocalCachePolicy{
		AllowedSizes: []string{"100Gi", "200Gi"}, DefaultSize: "200Gi", MaxSize: "500Gi", MountPath: "/mnt/cache",
	}})
	principal := auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	cache := decodePlatformLimits(t, response.Body.Bytes()).Cache
	if cache.Enabled || cache.DefaultMode != string(domain.CacheModeOff) || strings.Join(cache.Modes, ",") != "off" {
		t.Fatalf("unexpected disabled cache modes: %#v", cache)
	}
	if len(cache.AllowedSizes) != 0 || cache.DefaultSize != "" || cache.MaxSize != "" || cache.MountPath != "" {
		t.Fatalf("disabled cache policy leaked unavailable choices: %#v", cache)
	}
}

// The Portal renders GPU pickers from this payload. Hard-coding a fleet size in
// the UI is exactly the mismatch that lets a user submit a job the server will
// reject, so the mount paths and execution profiles ship with the limits.
func TestPlatformLimitsDescribeGovernedMountPathsAndExecutionProfiles(t *testing.T) {
	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "local", GPULimit: 24, GPUAvailable: 24}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "local", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	limits := decodePlatformLimits(t, response.Body.Bytes())
	if limits.MountPaths.Dataset != domain.DataMountInputPath || limits.MountPaths.Output != domain.DataMountOutputPath {
		t.Fatalf("expected governed mount paths, got %+v", limits.MountPaths)
	}
	if limits.MountPaths.Checkpoint != domain.DataMountCheckpointPath || limits.MountPaths.Workspace != domain.WorkspaceMountPath {
		t.Fatalf("expected checkpoint and workspace paths, got %+v", limits.MountPaths)
	}
	modes := map[string]executionProfileDescriptor{}
	for _, profile := range limits.ExecutionProfiles {
		modes[profile.Mode] = profile
	}
	single, ok := modes[string(domain.ExecutionModeTorchrun)]
	if !ok {
		t.Fatalf("expected a torchrun profile, got %+v", limits.ExecutionProfiles)
	}
	if single.MinGPUsPerWorker != 2 || single.MaxWorkerReplicas != 1 {
		t.Fatalf("torchrun is single-node multi-GPU, got %+v", single)
	}
}

// A profile the current fleet cannot satisfy must be reported as unavailable
// rather than silently offered: a two-node request on a one-node ceiling wastes
// a full submit round trip and reads as a platform failure to the user.
func TestPlatformLimitsMarkProfilesUnavailableBeyondTheFleetCeiling(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 1, MaxGPUsPerWorker: 8, MaxTotalGPUs: 8})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "local", GPULimit: 8, GPUAvailable: 8}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "local", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	for _, profile := range decodePlatformLimits(t, response.Body.Bytes()).ExecutionProfiles {
		if profile.Mode == string(domain.ExecutionModeRayTrain) && profile.Available {
			t.Fatalf("multi-node must be unavailable on a single-node ceiling, got %+v", profile)
		}
		if profile.Mode == string(domain.ExecutionModeTorchrun) && !profile.Available {
			t.Fatalf("single-node multi-GPU must stay available, got %+v", profile)
		}
	}
}

func TestPlatformLimitsApplyCallerTenantQuotaForEngineerAndTenantAdmin(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin} {
		t.Run(role, func(t *testing.T) {
			quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{
				TenantID: "team-a", GPULimit: 8, GPUUsed: 3, GPUAvailable: 5,
			}}
			handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
			principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{role}, AuthType: auth.AuthTypeLocal}
			response := httptest.NewRecorder()

			limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

			if response.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
			}
			limits := decodePlatformLimits(t, response.Body.Bytes())
			if limits.MaxWorkerReplicas != 2 || limits.MaxGPUsPerWorker != 5 || limits.MaxTotalGPUs != 5 {
				t.Fatalf("expected remaining-quota limits 2 workers x 5 GPUs with 5 total, got %+v", limits)
			}
			if len(quota.tenantIDs) != 1 || quota.tenantIDs[0] != principal.TenantID {
				t.Fatalf("quota lookup must use only the caller tenant %q, got %v", principal.TenantID, quota.tenantIDs)
			}
			tenantQuota := decodePlatformTenantQuota(t, response.Body.Bytes())
			if tenantQuota == nil || tenantQuota.GPULimit != 8 || tenantQuota.GPUUsed != 3 || tenantQuota.GPUAvailable != 5 {
				t.Fatalf("expected caller quota limit/used/available, got %+v", tenantQuota)
			}
			for _, profile := range limits.ExecutionProfiles {
				if profile.MaxWorkerReplicas > limits.MaxWorkerReplicas {
					t.Fatalf("profile exceeds caller worker limit: %+v", profile)
				}
				if profile.Available && (profile.MinGPUsPerWorker > limits.MaxGPUsPerWorker || profile.MinWorkerReplicas*profile.MinGPUsPerWorker > limits.MaxTotalGPUs) {
					t.Fatalf("available profile exceeds caller GPU limit: %+v", profile)
				}
			}
			if strings.Contains(response.Body.String(), `"tenantId"`) || strings.Contains(response.Body.String(), "other-team") {
				t.Fatalf("limits response must not expose a tenant identity: %s", response.Body.String())
			}
		})
	}
}

func TestPlatformLimitsReturnPhysicalCeilingsForSuperAdminWithoutTenantQuotaLookup(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "admin-team", GPULimit: 1, GPUAvailable: 1}, err: errors.New("must not be called")}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "admin-1", TenantID: "admin-team", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()

	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	limits := decodePlatformLimits(t, response.Body.Bytes())
	if limits.MaxWorkerReplicas != 2 || limits.MaxGPUsPerWorker != 8 || limits.MaxTotalGPUs != 16 {
		t.Fatalf("expected physical fleet ceilings, got %+v", limits)
	}
	if len(quota.tenantIDs) != 0 {
		t.Fatalf("fleet-management limits must not read a tenant quota, got lookups %v", quota.tenantIDs)
	}
	if strings.Contains(response.Body.String(), `"tenantQuota"`) {
		t.Fatalf("fleet-management limits must not include a tenant quota, got %s", response.Body.String())
	}
}

func TestPlatformLimitsMarkEveryProfileUnavailableForZeroTenantQuota(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 0, GPUAvailable: 0}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()

	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	limits := decodePlatformLimits(t, response.Body.Bytes())
	if limits.MaxWorkerReplicas != 0 || limits.MaxGPUsPerWorker != 0 || limits.MaxTotalGPUs != 0 {
		t.Fatalf("expected zero effective limits, got %+v", limits)
	}
	for _, profile := range limits.ExecutionProfiles {
		if profile.Available {
			t.Fatalf("zero-quota caller must not receive an available profile: %+v", profile)
		}
	}
}

func TestPlatformLimitsClampNegativeAvailableQuotaToZero(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 8, GPUUsed: 9, GPUAvailable: -1}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()

	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	limits := decodePlatformLimits(t, response.Body.Bytes())
	if limits.MaxWorkerReplicas != 0 || limits.MaxGPUsPerWorker != 0 || limits.MaxTotalGPUs != 0 {
		t.Fatalf("negative available quota must not produce positive limits, got %+v", limits)
	}
	tenantQuota := decodePlatformTenantQuota(t, response.Body.Bytes())
	if tenantQuota == nil || tenantQuota.GPULimit != 8 || tenantQuota.GPUUsed != 9 || tenantQuota.GPUAvailable != 0 {
		t.Fatalf("expected negative availability normalized to zero, got %+v", tenantQuota)
	}
}

func TestPlatformLimitsCapInconsistentAvailabilityByLimitMinusUsage(t *testing.T) {
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })

	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 8, GPUUsed: 3, GPUAvailable: 8}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()

	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	limits := decodePlatformLimits(t, response.Body.Bytes())
	if limits.MaxWorkerReplicas != 2 || limits.MaxGPUsPerWorker != 5 || limits.MaxTotalGPUs != 5 {
		t.Fatalf("inconsistent store availability must be capped to five remaining GPUs, got %+v", limits)
	}
	tenantQuota := decodePlatformTenantQuota(t, response.Body.Bytes())
	if tenantQuota == nil || tenantQuota.GPUAvailable != 5 {
		t.Fatalf("caller quota must expose at most limit minus usage, got %+v", tenantQuota)
	}
}

func TestPlatformLimitsRejectQuotaFromAnotherTenant(t *testing.T) {
	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "other-team", GPULimit: 16, GPUAvailable: 16}}
	handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
	principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()

	limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "QUOTA_QUERY_FAILED") {
		t.Fatalf("expected mismatched quota to fail closed, got %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "other-team") || strings.Contains(response.Body.String(), "maxTotalGpus") {
		t.Fatalf("mismatched quota response leaked tenant data or physical limits: %s", response.Body.String())
	}
}

func TestPlatformLimitsFailClosedWhenQuotaIsUnavailable(t *testing.T) {
	principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}

	t.Run("not configured", func(t *testing.T) {
		handler := NewHandler(&fakeJobRepository{}, Options{})
		response := httptest.NewRecorder()

		limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "QUOTA_UNAVAILABLE") {
			t.Fatalf("expected clear 503 quota error, got %d: %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "maxTotalGpus") {
			t.Fatalf("quota failure must not leak unrestricted physical limits: %s", response.Body.String())
		}
	})

	t.Run("lookup failed", func(t *testing.T) {
		quota := &fakePlatformQuotaStore{err: errors.New("database unavailable")}
		handler := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
		response := httptest.NewRecorder()

		limitsRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "QUOTA_QUERY_FAILED") {
			t.Fatalf("expected clear 500 quota error, got %d: %s", response.Code, response.Body.String())
		}
		if len(quota.tenantIDs) != 1 || quota.tenantIDs[0] != principal.TenantID {
			t.Fatalf("failed lookup must still target only caller tenant %q, got %v", principal.TenantID, quota.tenantIDs)
		}
		if strings.Contains(response.Body.String(), "maxTotalGpus") {
			t.Fatalf("quota failure must not leak unrestricted physical limits: %s", response.Body.String())
		}
	})
}

func TestPlatformLimitsRequireAuthentication(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{})
	response := httptest.NewRecorder()
	limitsRouter(handler, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}
