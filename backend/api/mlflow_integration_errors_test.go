package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

func TestMLflowIntegrationAPIRunReadErrorsAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"missing run", fmt.Errorf("internal run context: %w", observability.ErrMLflowRunNotFound), 404, "MLFLOW_RUN_NOT_FOUND"},
		{"upstream failure", errors.New("http://mlflow.internal/secret storage path"), 502, "MLFLOW_REQUEST_FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeIntegrationProvider{err: tc.err}
			resp := integrationRequest(integrationRouter(integrationPrincipal(), provider, newFakeMLflowDashboardStore()), http.MethodGet, "")
			if resp.Code != tc.status || !strings.Contains(resp.Body.String(), tc.code) || strings.Contains(resp.Body.String(), "internal") {
				t.Fatalf("unsafe read error: %d %s", resp.Code, resp.Body.String())
			}
			if provider.reads != 1 || resp.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("read was not routed through guarded provider")
			}
		})
	}
}

func TestMLflowIntegrationAPIProviderRejectsInvalidBatchSafely(t *testing.T) {
	audit := newFakeMLflowDashboardStore()
	provider := &fakeIntegrationProvider{err: fmt.Errorf("internal validation: %w", observability.ErrMLflowBatchInvalid)}
	resp := integrationRequest(integrationRouter(integrationPrincipal(), provider, audit), http.MethodPost, validIntegrationBatch)
	if resp.Code != 400 || !strings.Contains(resp.Body.String(), "MLFLOW_BATCH_INVALID") || strings.Contains(resp.Body.String(), "internal") {
		t.Fatalf("unsafe invalid-batch error: %d %s", resp.Code, resp.Body.String())
	}
	if len(audit.audits) != 2 || audit.audits[1].Status != 400 {
		t.Fatal("provider rejection was not audited")
	}
}

func integrationHandlerRouter(handler *Handler) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", integrationPrincipal()); c.Next() })
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func TestMLflowIntegrationAPICompatibilityProviderUnavailable(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-a"}}}, Options{Experiments: &fakeExperimentProvider{}})
		resp := integrationRequest(integrationHandlerRouter(handler), method, validIntegrationBatch)
		if resp.Code != 503 || !strings.Contains(resp.Body.String(), "MLFLOW_UNAVAILABLE") {
			t.Fatalf("legacy provider did not fail safely: %d %s", resp.Code, resp.Body.String())
		}
	}
}

func TestMLflowIntegrationAPIMissingAuditStorePreventsWrite(t *testing.T) {
	provider := &fakeIntegrationProvider{}
	handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-a"}}}, Options{Experiments: provider})
	resp := integrationRequest(integrationHandlerRouter(handler), http.MethodPost, validIntegrationBatch)
	if resp.Code != 503 || !strings.Contains(resp.Body.String(), "MLFLOW_AUDIT_UNAVAILABLE") || provider.writes != 0 {
		t.Fatalf("write without audit store: %d %s", resp.Code, resp.Body.String())
	}
}

type integrationCompletionFailStore struct{ *fakeMLflowDashboardStore }

func (store *integrationCompletionFailStore) CreateMLflowAuditLog(ctx context.Context, event repositories.MLflowAuditEvent) error {
	if event.Status != http.StatusProcessing {
		return errors.New("private database detail")
	}
	return store.fakeMLflowDashboardStore.CreateMLflowAuditLog(ctx, event)
}

func TestMLflowIntegrationAPIFailedCompletionAuditReportsUncertainWrite(t *testing.T) {
	audit := &integrationCompletionFailStore{newFakeMLflowDashboardStore()}
	provider := &fakeIntegrationProvider{audit: audit.fakeMLflowDashboardStore}
	handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", UserID: "user-a"}}}, Options{Experiments: provider, MLflowDashboardStore: audit})
	resp := integrationRequest(integrationHandlerRouter(handler), http.MethodPost, validIntegrationBatch)
	if resp.Code != 503 || provider.writes != 1 || !strings.Contains(resp.Body.String(), "MLFLOW_AUDIT_INCOMPLETE") || !strings.Contains(resp.Body.String(), "verify the run before retrying") || strings.Contains(resp.Body.String(), "private database") {
		t.Fatalf("uncertain write was misreported: %d %s", resp.Code, resp.Body.String())
	}
}

func TestMLflowIntegrationAPIMalformedRunIDNeverReachesProvider(t *testing.T) {
	provider := &fakeIntegrationProvider{}
	router := integrationRouter(integrationPrincipal(), provider, newFakeMLflowDashboardStore())
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/mlflow/runs/not-a-run-id", nil))
	if resp.Code != 404 || provider.reads != 0 || !strings.Contains(resp.Body.String(), "MLFLOW_RUN_NOT_FOUND") {
		t.Fatalf("malformed run accepted: %d %s", resp.Code, resp.Body.String())
	}
}

func TestMLflowIntegrationGuardRequiresIdentityEvenWhenUsedDirectly(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{})
	router := gin.New()
	called := false
	router.GET("/runs/:runId", handler.mlflowIntegrationGuard(newFixedWindowSourceArtifactLimiter(1, 1, 10, time.Now), false), func(c *gin.Context) { called = true; c.Status(200) })
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/runs/0123456789abcdef0123456789abcdef", nil))
	if resp.Code != 401 || called {
		t.Fatalf("guard accepted anonymous caller: %d", resp.Code)
	}
}
