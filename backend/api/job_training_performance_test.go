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

type fakeTrainingPerformanceProvider struct {
	calls  int
	ref    domain.TrainingWorkloadRef
	window string
	result domain.TrainingPerformance
	err    error
}

func (p *fakeTrainingPerformanceProvider) QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error) {
	return observability.JobMetrics{}, nil
}
func (p *fakeTrainingPerformanceProvider) QueryTrainingPerformance(_ context.Context, ref domain.TrainingWorkloadRef, window string) (domain.TrainingPerformance, error) {
	p.calls++
	p.ref = ref
	p.window = window
	return p.result, p.err
}

func trainingPerformanceRouter(handler *Handler, principal *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if principal != nil {
		identity := *principal
		router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", identity); c.Next() })
	}
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func TestTrainingPerformanceUsesPersistedSelectorsAndRejectsClientSelectors(t *testing.T) {
	job := domain.TrainingJob{ID: "job-a", TenantID: "team-a", UserID: "owner-a", KubernetesNS: "tenant-team-a", RayClusterName: "persisted-cluster", RayJobName: "persisted-rayjob", ClusterAttempt: 2, WorkerRestartCount: 3, ResumeCheckpointID: "cp-1"}
	principal := auth.Principal{Subject: "owner-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}
	provider := &fakeTrainingPerformanceProvider{result: domain.TrainingPerformance{Workers: []domain.TrainingWorkerPerformance{}, Series: map[string][]domain.TrainingMetricSeries{}, Summary: map[string]*float64{}}}
	handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Metrics: provider})

	response := httptest.NewRecorder()
	trainingPerformanceRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/training-performance?window=1h", nil))
	if response.Code != http.StatusOK || provider.calls != 1 || provider.ref.Namespace != job.KubernetesNS || provider.ref.RayClusterName != job.RayClusterName || provider.ref.RayJobName != job.RayJobName || provider.window != "1h" {
		t.Fatalf("persisted selectors not used: status=%d provider=%+v body=%s", response.Code, provider, response.Body.String())
	}
	var envelope struct {
		Data domain.TrainingPerformance `json:"data"`
	}
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || len(envelope.Data.Recovery) == 0 || envelope.Data.Recovery[0].ClusterAttempt != 2 || envelope.Data.Recovery[0].RestartCount != 3 || envelope.Data.Recovery[0].ResumeCheckpointID != "cp-1" {
		t.Fatalf("persisted recovery snapshot missing: %s", response.Body.String())
	}

	for _, query := range []string{"namespace=attacker", "rayClusterName=forged", "task=worker-0", "pod=forged"} {
		response = httptest.NewRecorder()
		trainingPerformanceRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/training-performance?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("client selector %q accepted: %d %s", query, response.Code, response.Body.String())
		}
	}
}

func TestTrainingPerformanceAuthorizationBoundary(t *testing.T) {
	job := domain.TrainingJob{ID: "job-a", TenantID: "team-a", UserID: "owner-a", KubernetesNS: "tenant-team-a", RayClusterName: "cluster-a", RayJobName: "rayjob-a"}
	cases := []struct {
		name      string
		principal auth.Principal
		want      int
		calls     int
	}{
		{"owner", auth.Principal{Subject: "owner-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}, 200, 1},
		{"same team engineer", auth.Principal{Subject: "other", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}, 404, 0},
		{"team admin", auth.Principal{Subject: "admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeOIDC}, 200, 1},
		{"cross tenant", auth.Principal{Subject: "owner-a", TenantID: "team-b", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}, 404, 0},
		{"admin", auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}, 200, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeTrainingPerformanceProvider{result: domain.TrainingPerformance{Workers: []domain.TrainingWorkerPerformance{}, Series: map[string][]domain.TrainingMetricSeries{}, Summary: map[string]*float64{}}}
			handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Metrics: provider})
			response := httptest.NewRecorder()
			trainingPerformanceRouter(handler, &tc.principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/training-performance", nil))
			if response.Code != tc.want || provider.calls != tc.calls {
				t.Fatalf("want status=%d calls=%d got status=%d calls=%d body=%s", tc.want, tc.calls, response.Code, provider.calls, response.Body.String())
			}
		})
	}
}

func TestTrainingPerformanceValidatesWindowAndHidesProviderDetails(t *testing.T) {
	job := domain.TrainingJob{ID: "job-a", TenantID: "team-a", UserID: "owner-a", KubernetesNS: "tenant-a", RayClusterName: "cluster-a", RayJobName: "rayjob-a"}
	principal := auth.Principal{Subject: "owner-a", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}
	provider := &fakeTrainingPerformanceProvider{err: errors.New("dial http://prometheus.internal:9090 with bearer secret")}
	handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Metrics: provider})
	for _, window := range []string{"7d", "1h%0Aup", ""} {
		path := "/api/v1/jobs/job-a/training-performance?window=" + window
		response := httptest.NewRecorder()
		trainingPerformanceRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if window == "" {
			if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "prometheus.internal") || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("provider detail leaked: %d %s", response.Code, response.Body.String())
			}
			continue
		}
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid window accepted: %q status=%d body=%s", window, response.Code, response.Body.String())
		}
	}
}
