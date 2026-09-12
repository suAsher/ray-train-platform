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
	"ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/repositories"
)

type fakeMLflowTrackingService struct {
	actor          mlflowtracking.Actor
	createKey      string
	createName     string
	createRunKey   string
	createRunName  string
	experimentID   string
	runID          string
	limit          int
	cursor         string
	loggedBatch    mlflowtracking.Batch
	finishStatus   string
	err            error
	experimentPage mlflowtracking.ExperimentPage
	runPage        mlflowtracking.RunPage
	experiment     mlflowtracking.Experiment
	run            mlflowtracking.Run
	detail         mlflowtracking.RunDetail
}

func (s *fakeMLflowTrackingService) CreateExperiment(_ context.Context, actor mlflowtracking.Actor, idempotencyKey, name string) (mlflowtracking.Experiment, error) {
	s.actor, s.createKey, s.createName = actor, idempotencyKey, name
	return s.experiment, s.err
}

func (s *fakeMLflowTrackingService) CreateRun(_ context.Context, actor mlflowtracking.Actor, experimentID, idempotencyKey, name string) (mlflowtracking.Run, error) {
	s.actor, s.experimentID, s.createRunKey, s.createRunName = actor, experimentID, idempotencyKey, name
	return s.run, s.err
}

func (s *fakeMLflowTrackingService) ListExperiments(_ context.Context, actor mlflowtracking.Actor, limit int, cursor string) (mlflowtracking.ExperimentPage, error) {
	s.actor, s.limit, s.cursor = actor, limit, cursor
	return s.experimentPage, s.err
}

func (s *fakeMLflowTrackingService) ListRuns(_ context.Context, actor mlflowtracking.Actor, experimentID string, limit int, cursor string) (mlflowtracking.RunPage, error) {
	s.actor, s.experimentID, s.limit, s.cursor = actor, experimentID, limit, cursor
	return s.runPage, s.err
}

func (s *fakeMLflowTrackingService) GetRun(_ context.Context, actor mlflowtracking.Actor, runID string) (mlflowtracking.RunDetail, error) {
	s.actor, s.runID = actor, runID
	return s.detail, s.err
}

func (s *fakeMLflowTrackingService) LogRun(_ context.Context, actor mlflowtracking.Actor, runID string, batch mlflowtracking.Batch) error {
	s.actor, s.runID, s.loggedBatch = actor, runID, batch
	return s.err
}

func (s *fakeMLflowTrackingService) FinishRun(_ context.Context, actor mlflowtracking.Actor, runID, status string) (mlflowtracking.Run, error) {
	s.actor, s.runID, s.finishStatus = actor, runID, status
	return s.run, s.err
}

func trackingPrincipal(scopes ...string) auth.Principal {
	if len(scopes) == 0 {
		scopes = []string{domain.PATScopeExperimentsRead}
	}
	return auth.Principal{Subject: "user-a", Username: "engineer", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypePAT, Scopes: scopes}
}

func trackingRouter(principal auth.Principal, service mlflowTrackingService, audit *fakeMLflowDashboardStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&fakeJobRepository{}, Options{MLflowTracking: service, MLflowDashboardStore: audit})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func trackingRequest(router http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeTrackingData(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, response.Body.String())
	}
	if !envelope.Success {
		t.Fatalf("response was not successful: %s", response.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode data: %v body=%s", err, response.Body.String())
	}
}

func TestMLflowTrackingCapabilitiesAreTruthfulAndDowngradeable(t *testing.T) {
	oidc := trackingPrincipal()
	oidc.AuthType = auth.AuthTypeOIDC
	oidc.Scopes = nil
	empty := trackingRequest(trackingRouter(oidc, nil, nil), http.MethodGet, "/api/v1/mlflow/capabilities", "", "")
	if empty.Code != http.StatusOK {
		t.Fatalf("expected capability downgrade success, got %d %s", empty.Code, empty.Body.String())
	}
	var unavailable struct {
		Available     bool `json:"available"`
		Read          bool `json:"read"`
		Write         bool `json:"write"`
		SDKCompatible bool `json:"sdkCompatible"`
		Supports      struct {
			ExperimentTracking bool `json:"experimentTracking"`
			ArtifactManagement bool `json:"artifactManagement"`
			ModelRegistry      bool `json:"modelRegistry"`
			ModelEvaluation    bool `json:"modelEvaluation"`
			ModelServing       bool `json:"modelServing"`
		} `json:"supports"`
	}
	decodeTrackingData(t, empty, &unavailable)
	if unavailable.Available || unavailable.Read || unavailable.Write || unavailable.SDKCompatible || unavailable.Supports.ModelRegistry || unavailable.Supports.ModelServing {
		t.Fatalf("capabilities must not advertise unavailable or unimplemented features: %+v", unavailable)
	}

	withWrite := trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite)
	service := &fakeMLflowTrackingService{}
	ready := trackingRequest(trackingRouter(withWrite, service, nil), http.MethodGet, "/api/v1/mlflow/capabilities", "", "")
	var available struct {
		Available                bool     `json:"available"`
		Read                     bool     `json:"read"`
		Write                    bool     `json:"write"`
		SDKCompatible            bool     `json:"sdkCompatible"`
		SDKBasePath              string   `json:"sdkBasePath"`
		SDKProtocol              string   `json:"sdkProtocol"`
		SDKClientVersion         string   `json:"sdkClientVersion"`
		SDKMethods               []string `json:"sdkMethods"`
		SDKRequiresPrecreatedRun bool     `json:"sdkRequiresPrecreatedRun"`
		Scopes                   struct {
			Read  string `json:"read"`
			Write string `json:"write"`
		} `json:"scopes"`
		Supports struct {
			ExperimentTracking bool `json:"experimentTracking"`
			ArtifactManagement bool `json:"artifactManagement"`
			ModelRegistry      bool `json:"modelRegistry"`
			ModelEvaluation    bool `json:"modelEvaluation"`
			ModelServing       bool `json:"modelServing"`
		} `json:"supports"`
	}
	if ready.Code != http.StatusOK {
		t.Fatalf("expected ready capabilities, got %d %s", ready.Code, ready.Body.String())
	}
	decodeTrackingData(t, ready, &available)
	if !available.Available || !available.Read || !available.Write || available.Scopes.Read != "experiments:read" || available.Scopes.Write != "experiments:write" {
		t.Fatalf("unexpected capability payload: %+v", available)
	}
	if available.SDKCompatible || available.SDKBasePath != "" || available.SDKProtocol != "tracking-subset" || available.SDKClientVersion != "3.14.0" || len(available.SDKMethods) != 6 || !available.SDKRequiresPrecreatedRun {
		t.Fatalf("REST-only capabilities must describe an unregistered SDK subset without enabling it: %+v", available)
	}
	if !available.Supports.ExperimentTracking || available.Supports.ArtifactManagement || available.Supports.ModelRegistry || available.Supports.ModelEvaluation || available.Supports.ModelServing {
		t.Fatalf("capabilities must only advertise implemented tracking support: %+v", available.Supports)
	}

	handler := NewHandler(&fakeJobRepository{}, Options{MLflowTracking: service})
	handler.mlflowSDKRegistered = true
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", withWrite)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	registered := trackingRequest(router, http.MethodGet, "/api/v1/mlflow/capabilities", "", "")
	if registered.Code != http.StatusOK {
		t.Fatalf("expected registered capability success, got %d %s", registered.Code, registered.Body.String())
	}
	var sdkReady struct {
		SDKCompatible bool   `json:"sdkCompatible"`
		SDKBasePath   string `json:"sdkBasePath"`
	}
	decodeTrackingData(t, registered, &sdkReady)
	if !sdkReady.SDKCompatible || sdkReady.SDKBasePath != "/api/v1/mlflow-tracking" {
		t.Fatalf("SDK registration was not reflected in capabilities: %+v", sdkReady)
	}
}

func TestMLflowTrackingListExperimentsUsesActorAndBoundedPagination(t *testing.T) {
	service := &fakeMLflowTrackingService{experimentPage: mlflowtracking.ExperimentPage{
		Items:      []mlflowtracking.Experiment{{ID: "11111111111111111111111111111111", Name: "external-eval", State: "ACTIVE", CreatedAt: time.Unix(100, 0).UTC(), UpstreamID: "12"}},
		NextCursor: "signed-next",
	}}
	response := trackingRequest(trackingRouter(trackingPrincipal(), service, nil), http.MethodGet, "/api/v1/mlflow/experiments?limit=500&cursor=signed-current", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", response.Code, response.Body.String())
	}
	if service.actor.TenantID != "team-a" || service.actor.UserID != "user-a" || service.limit != 100 || service.cursor != "signed-current" {
		t.Fatalf("list did not pass bounded owner query: actor=%+v limit=%d cursor=%q", service.actor, service.limit, service.cursor)
	}
	var page struct {
		Items      []mlflowtracking.Experiment `json:"items"`
		NextCursor string                      `json:"nextCursor"`
	}
	decodeTrackingData(t, response, &page)
	if len(page.Items) != 1 || page.Items[0].UpstreamID != "12" || page.NextCursor != "signed-next" {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestMLflowTrackingCreateExperimentRequiresPATWriteAndIdempotencyKey(t *testing.T) {
	service := &fakeMLflowTrackingService{experiment: mlflowtracking.Experiment{ID: "11111111111111111111111111111111", Name: "external-eval", State: "ACTIVE", CreatedAt: time.Unix(100, 0).UTC(), UpstreamID: "12"}}
	noWrite := trackingRequest(trackingRouter(trackingPrincipal(domain.PATScopeExperimentsRead), service, nil), http.MethodPost, "/api/v1/mlflow/experiments", `{"name":"external-eval"}`, "idem-1")
	if noWrite.Code != http.StatusForbidden {
		t.Fatalf("expected missing write scope denied, got %d", noWrite.Code)
	}
	browser := trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite)
	browser.AuthType = auth.AuthTypeOIDC
	noPAT := trackingRequest(trackingRouter(browser, service, nil), http.MethodPost, "/api/v1/mlflow/experiments", `{"name":"external-eval"}`, "idem-1")
	if noPAT.Code != http.StatusForbidden {
		t.Fatalf("expected browser write denied, got %d", noPAT.Code)
	}
	missingKey := trackingRequest(trackingRouter(trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite), service, nil), http.MethodPost, "/api/v1/mlflow/experiments", `{"name":"external-eval"}`, "")
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing idempotency key rejected, got %d", missingKey.Code)
	}
	nonASCIIKey := trackingRequest(trackingRouter(trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite), service, nil), http.MethodPost, "/api/v1/mlflow/experiments", `{"name":"external-eval"}`, "幂等")
	if nonASCIIKey.Code != http.StatusBadRequest {
		t.Fatalf("expected non-ASCII idempotency key rejected, got %d", nonASCIIKey.Code)
	}
	created := trackingRequest(trackingRouter(trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite), service, newFakeMLflowDashboardStore()), http.MethodPost, "/api/v1/mlflow/experiments", `{"name":"external-eval"}`, "idem-1")
	if created.Code != http.StatusCreated || service.createKey != "idem-1" || service.createName != "external-eval" {
		t.Fatalf("unexpected create result status=%d key=%q name=%q body=%s", created.Code, service.createKey, service.createName, created.Body.String())
	}
}

func TestMLflowTrackingRunsLifecycleAndAudit(t *testing.T) {
	audit := newFakeMLflowDashboardStore()
	service := &fakeMLflowTrackingService{
		runPage: mlflowtracking.RunPage{Items: []mlflowtracking.Run{{ID: "22222222222222222222222222222222", ExperimentID: "11111111111111111111111111111111", Name: "candidate", State: "RUNNING", CreatedAt: time.Unix(200, 0).UTC(), UpstreamID: "0123456789abcdef0123456789abcdef"}}, NextCursor: "next-runs"},
		run:     mlflowtracking.Run{ID: "22222222222222222222222222222222", ExperimentID: "11111111111111111111111111111111", Name: "candidate", State: "FINISHED", CreatedAt: time.Unix(200, 0).UTC(), FinishedAt: timePtr(time.Unix(300, 0).UTC()), UpstreamID: "0123456789abcdef0123456789abcdef"},
		detail: mlflowtracking.RunDetail{
			Run:    mlflowtracking.Run{ID: "22222222222222222222222222222222", ExperimentID: "11111111111111111111111111111111", Name: "candidate", State: "RUNNING", CreatedAt: time.Unix(200, 0).UTC(), UpstreamID: "0123456789abcdef0123456789abcdef"},
			Latest: map[string]float64{"external/quality_score": 0.91},
			Params: map[string]string{"external.evaluator_version": "v1"},
		},
	}
	principal := trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite)
	router := trackingRouter(principal, service, audit)

	runs := trackingRequest(router, http.MethodGet, "/api/v1/mlflow/experiments/11111111111111111111111111111111/runs?limit=25&cursor=signed", "", "")
	if runs.Code != http.StatusOK || service.experimentID != "11111111111111111111111111111111" || service.limit != 25 || service.cursor != "signed" {
		t.Fatalf("unexpected list runs status=%d exp=%q limit=%d cursor=%q", runs.Code, service.experimentID, service.limit, service.cursor)
	}
	create := trackingRequest(router, http.MethodPost, "/api/v1/mlflow/experiments/11111111111111111111111111111111/runs", `{"name":"candidate"}`, "idem-run")
	if create.Code != http.StatusCreated || service.createRunKey != "idem-run" || service.createRunName != "candidate" {
		t.Fatalf("unexpected create run status=%d key=%q name=%q", create.Code, service.createRunKey, service.createRunName)
	}
	get := trackingRequest(router, http.MethodGet, "/api/v1/mlflow/runs/22222222222222222222222222222222", "", "")
	if get.Code != http.StatusOK || service.runID != "22222222222222222222222222222222" {
		t.Fatalf("unexpected get run status=%d run=%q", get.Code, service.runID)
	}
	log := trackingRequest(router, http.MethodPost, "/api/v1/mlflow/runs/22222222222222222222222222222222/log-batch", `{"metrics":[{"key":"external/quality_score","value":0.91,"timestamp":1000,"step":1}],"params":[{"key":"external.evaluator_version","value":"v1"}]}`, "")
	if log.Code != http.StatusOK || service.runID != "22222222222222222222222222222222" || len(service.loggedBatch.Metrics) != 1 || len(audit.audits) != 4 {
		t.Fatalf("unexpected log result status=%d run=%q batch=%+v audits=%d body=%s", log.Code, service.runID, service.loggedBatch, len(audit.audits), log.Body.String())
	}
	finish := trackingRequest(router, http.MethodPost, "/api/v1/mlflow/runs/22222222222222222222222222222222/finish", `{"status":"FINISHED"}`, "")
	if finish.Code != http.StatusOK || service.finishStatus != "FINISHED" || len(audit.audits) != 6 {
		t.Fatalf("unexpected finish result status=%d status=%q audits=%d", finish.Code, service.finishStatus, len(audit.audits))
	}
}

func TestMLflowTrackingStrictBodiesAndErrors(t *testing.T) {
	service := &fakeMLflowTrackingService{}
	router := trackingRouter(trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite), service, newFakeMLflowDashboardStore())
	for _, request := range []struct {
		method string
		path   string
		body   string
		key    string
	}{
		{http.MethodPost, "/api/v1/mlflow/experiments", `{"name":"external","extra":true}`, "idem"},
		{http.MethodPost, "/api/v1/mlflow/experiments", `{"name":""}`, "idem"},
		{http.MethodPost, "/api/v1/mlflow/experiments/11111111111111111111111111111111/runs", `{"name":"candidate","extra":true}`, "idem"},
		{http.MethodPost, "/api/v1/mlflow/runs/22222222222222222222222222222222/log-batch", `{"Metrics":[{"key":"external/quality_score","value":1,"timestamp":1,"step":0}]}`, ""},
		{http.MethodPost, "/api/v1/mlflow/runs/22222222222222222222222222222222/finish", `{"status":"RUNNING"}`, ""},
	} {
		response := trackingRequest(router, request.method, request.path, request.body, request.key)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s %s expected 400, got %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}

	service.err = mlflowtracking.ErrNotFound
	missing := trackingRequest(router, http.MethodGet, "/api/v1/mlflow/runs/33333333333333333333333333333333", "", "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "MLFLOW_TRACKING_NOT_FOUND") {
		t.Fatalf("not found not mapped safely: %d %s", missing.Code, missing.Body.String())
	}
	service.err = errors.New("s3://private upstream secret")
	failed := trackingRequest(router, http.MethodGet, "/api/v1/mlflow/runs/22222222222222222222222222222222", "", "")
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "s3://") {
		t.Fatalf("unexpected generic error: %d %s", failed.Code, failed.Body.String())
	}
}

func TestMLflowTrackingAuditFailureBlocksWrites(t *testing.T) {
	audit := newFakeMLflowDashboardStore()
	audit.auditErr = errors.New("db unavailable")
	service := &fakeMLflowTrackingService{}
	router := trackingRouter(trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeExperimentsWrite), service, audit)
	response := trackingRequest(router, http.MethodPost, "/api/v1/mlflow/runs/22222222222222222222222222222222/log-batch", `{"metrics":[{"key":"external/quality_score","value":1,"timestamp":1000,"step":0}]}`, "")
	if response.Code != http.StatusServiceUnavailable || len(service.loggedBatch.Metrics) != 0 {
		t.Fatalf("unaudited log reached service: status=%d batch=%+v", response.Code, service.loggedBatch)
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}

var _ mlflowTrackingService = (*fakeMLflowTrackingService)(nil)
var _ MLflowDashboardStore = (*fakeMLflowDashboardStore)(nil)
var _ = repositories.MLflowAuditRunLogBatch
