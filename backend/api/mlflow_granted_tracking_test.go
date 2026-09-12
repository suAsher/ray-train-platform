package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/integrations"
	"ray-train-platform-backend/mlflowtracking"
)

type integrationAccessFake struct {
	denied     bool
	seen       auth.Principal
	permission string
	ids        []string
	created    string
}

func (f *integrationAccessFake) Resolve(_ context.Context, p auth.Principal) (integrations.Identity, error) {
	f.seen = p
	if f.denied {
		return integrations.Identity{}, integrations.ErrNotFound
	}
	return integrations.Identity{ID: p.IntegrationID, TenantID: p.TenantID, OwnerUserID: "owner", AllowCreateExperiments: true}, nil
}
func (f *integrationAccessFake) Authorize(ctx context.Context, p auth.Principal, id, permission string) (integrations.Identity, error) {
	f.permission = permission
	return f.Resolve(ctx, p)
}
func (f *integrationAccessFake) ListGrantedExperimentIDs(ctx context.Context, p auth.Principal, permission string) ([]string, error) {
	_, err := f.Resolve(ctx, p)
	return f.ids, err
}
func (f *integrationAccessFake) GrantCreated(_ context.Context, p auth.Principal, id string) error {
	f.created = id
	return nil
}

type integrationTrackingRecordsFake struct {
	run        mlflowtracking.Run
	experiment mlflowtracking.Experiment
}

func (f integrationTrackingRecordsFake) GetRun(_ context.Context, a mlflowtracking.Actor, id string) (mlflowtracking.Run, error) {
	if a.UserID != "owner" {
		return mlflowtracking.Run{}, mlflowtracking.ErrNotFound
	}
	return f.run, nil
}
func (f integrationTrackingRecordsFake) GetExperiment(_ context.Context, a mlflowtracking.Actor, id string) (mlflowtracking.Experiment, error) {
	return f.experiment, nil
}

func TestGrantedTrackingRejectsBeforeUpstream(t *testing.T) {
	upstream := &fakeMLflowTrackingService{}
	access := &integrationAccessFake{denied: true}
	service := NewGrantedMLflowTracking(upstream, integrationTrackingRecordsFake{}, access, []byte(strings.Repeat("k", 32)))
	actor := mlflowtracking.Actor{TenantID: "team", UserID: "integration:" + strings.Repeat("a", 32), IntegrationID: strings.Repeat("a", 32)}
	_, err := service.GetRun(machineTestContext(actor), actor, strings.Repeat("b", 32))
	if !errors.Is(err, mlflowtracking.ErrNotFound) || upstream.runID != "" {
		t.Fatalf("denied machine reached upstream: %v %s", err, upstream.runID)
	}
}

func TestGrantedTrackingPreservesHumanAndNamespacesMachine(t *testing.T) {
	upstream := &fakeMLflowTrackingService{experiment: mlflowtracking.Experiment{ID: strings.Repeat("c", 32), State: "READY"}}
	access := &integrationAccessFake{}
	service := NewGrantedMLflowTracking(upstream, integrationTrackingRecordsFake{}, access, []byte(strings.Repeat("k", 32)))
	human := mlflowtracking.Actor{TenantID: "team", UserID: "owner"}
	if _, err := service.CreateExperiment(context.Background(), human, "same-key", "Human"); err != nil || upstream.createKey != "same-key" || upstream.actor != human {
		t.Fatal("human contract changed", err)
	}
	machine := mlflowtracking.Actor{TenantID: "team", UserID: "integration:" + strings.Repeat("a", 32), IntegrationID: strings.Repeat("a", 32)}
	if _, err := service.CreateExperiment(machineTestContext(machine), machine, "same-key", "Machine"); err != nil {
		t.Fatal(err)
	}
	firstKey := upstream.createKey
	if upstream.actor.UserID != "owner" || upstream.actor.IntegrationID != "" || firstKey == "same-key" || len(firstKey) > 128 || access.seen.Subject != machine.UserID || access.created != upstream.experiment.ID {
		t.Fatal("machine delegation lost identity or grant", upstream.actor)
	}
	other := machine
	other.IntegrationID = strings.Repeat("d", 32)
	other.UserID = "integration:" + other.IntegrationID
	if _, err := service.CreateExperiment(machineTestContext(other), other, "same-key", "Machine"); err != nil || upstream.createKey == firstKey {
		t.Fatal("keys collide between machines", err)
	}
}

func TestGrantedTrackingChecksWriteGrantAndCursorIdentity(t *testing.T) {
	runID, expID := strings.Repeat("b", 32), strings.Repeat("c", 32)
	upstream := &fakeMLflowTrackingService{runPage: mlflowtracking.RunPage{NextCursor: "owner-private-cursor"}}
	access := &integrationAccessFake{}
	records := integrationTrackingRecordsFake{run: mlflowtracking.Run{ID: runID, ExperimentID: expID}}
	service := NewGrantedMLflowTracking(upstream, records, access, []byte(strings.Repeat("k", 32)))
	actor := mlflowtracking.Actor{TenantID: "team", UserID: "integration:" + strings.Repeat("a", 32), IntegrationID: strings.Repeat("a", 32)}
	if err := service.LogRun(machineTestContext(actor), actor, runID, mlflowtracking.Batch{}); err != nil || access.permission != "write" {
		t.Fatal("write grant not checked", err)
	}
	page, err := service.ListRuns(machineTestContext(actor), actor, expID, 1, "")
	if err != nil || page.NextCursor == "owner-private-cursor" || page.NextCursor == "" {
		t.Fatal("owner cursor exposed", err)
	}
	other := actor
	other.IntegrationID = strings.Repeat("d", 32)
	other.UserID = "integration:" + other.IntegrationID
	if _, err = service.ListRuns(machineTestContext(other), other, expID, 1, page.NextCursor); !errors.Is(err, mlflowtracking.ErrInvalid) {
		t.Fatal("cross identity cursor accepted", err)
	}
	access.denied = true
	if _, err = service.ListRuns(machineTestContext(actor), actor, expID, 1, page.NextCursor); !errors.Is(err, mlflowtracking.ErrNotFound) {
		t.Fatal("revoked grant cursor usable", err)
	}
}

func machineTestContext(a mlflowtracking.Actor) context.Context {
	return auth.SetPrincipalContext(context.Background(), auth.Principal{Subject: a.UserID, TenantID: a.TenantID, IntegrationID: a.IntegrationID, AuthType: auth.AuthTypePAT, Scopes: []string{"experiments:read", "experiments:write", "artifacts:read", "artifacts:write"}})
}

type grantedTimedTrackingFake struct {
	*fakeMLflowTrackingService
	finishCalls      int
	endTimeMS        int64
	contextPrincipal auth.Principal
}

func (f *grantedTimedTrackingFake) FinishRunAt(ctx context.Context, actor mlflowtracking.Actor, id, status string, endTimeMS int64) (mlflowtracking.Run, error) {
	f.finishCalls++
	f.endTimeMS = endTimeMS
	f.contextPrincipal, _ = auth.PrincipalFromContext(ctx)
	run, err := f.fakeMLflowTrackingService.FinishRun(ctx, actor, id, status)
	run.EndTimeMS = endTimeMS
	return run, err
}

type grantedFinishAccessFake struct {
	integrationAccessFake
	denyGrant bool
}

func (f *grantedFinishAccessFake) Authorize(ctx context.Context, p auth.Principal, id, permission string) (integrations.Identity, error) {
	f.permission = permission
	if f.denyGrant {
		return integrations.Identity{}, integrations.ErrNotFound
	}
	return f.integrationAccessFake.Authorize(ctx, p, id, permission)
}

// Register the actual SDK adapter against the production wrapper, preserving
// both principal locations populated by the authentication middleware.
func grantedSDKTestRouter(p auth.Principal, service mlflowTrackingService, audit *fakeMLflowDashboardStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", p)
		c.Request = c.Request.WithContext(auth.SetPrincipalContext(c.Request.Context(), p))
		c.Next()
	})
	h := NewHandler(&fakeJobRepository{}, Options{MLflowTracking: service, MLflowDashboardStore: audit})
	h.RegisterMLflowSDKRoutes(r.Group("/api/v1"))
	return r
}

func TestGrantedTrackingImplementsSDKTimedFinisher(t *testing.T) {
	service := NewGrantedMLflowTracking(&fakeMLflowTrackingService{}, integrationTrackingRecordsFake{}, &integrationAccessFake{}, []byte(strings.Repeat("k", 32)))
	if _, ok := any(service).(mlflowTrackingTimedFinisher); !ok {
		t.Fatal("production wrapper hides FinishRunAt from the MLflow SDK adapter")
	}
}

func TestGrantedTrackingSDKUpdatePreservesEndTimeAndAuditIdentity(t *testing.T) {
	for _, machine := range []bool{false, true} {
		t.Run(fmt.Sprintf("machine=%t", machine), func(t *testing.T) {
			p := trackingPrincipal("experiments:read", "experiments:write")
			ownerID := p.Subject
			if machine {
				p.IntegrationID = strings.Repeat("a", 32)
				p.Subject = "integration:" + p.IntegrationID
				ownerID = "owner"
			}
			runID, expID := strings.Repeat("b", 32), strings.Repeat("c", 32)
			upstream := &grantedTimedTrackingFake{fakeMLflowTrackingService: &fakeMLflowTrackingService{run: mlflowtracking.Run{ID: runID, ExperimentID: expID, State: "FINISHED", StartTimeMS: 1000}}}
			access := &grantedFinishAccessFake{}
			service := NewGrantedMLflowTracking(upstream, integrationTrackingRecordsFake{run: mlflowtracking.Run{ID: runID, ExperimentID: expID}}, access, []byte(strings.Repeat("k", 32)))
			audit := newFakeMLflowDashboardStore()
			response := httptest.NewRecorder()
			const endTimeMS int64 = 1789187654321
			body := fmt.Sprintf(`{"run_id":%q,"status":"FINISHED","end_time":%d}`, runID, endTimeMS)
			grantedSDKTestRouter(p, service, audit).ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/update", strings.NewReader(body)))
			if response.Code != 200 {
				t.Fatalf("SDK update through wrapper returned %d: %s", response.Code, response.Body.String())
			}
			var result struct {
				RunInfo struct {
					RunID   string `json:"run_id"`
					EndTime int64  `json:"end_time"`
				} `json:"run_info"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if upstream.finishCalls != 1 || upstream.endTimeMS != endTimeMS || result.RunInfo.EndTime != endTimeMS || result.RunInfo.RunID != runID {
				t.Fatalf("SDK end_time was dropped or replaced: calls=%d passed=%d returned=%d", upstream.finishCalls, upstream.endTimeMS, result.RunInfo.EndTime)
			}
			if upstream.actor.UserID != ownerID || upstream.actor.TenantID != p.TenantID || upstream.actor.IntegrationID != "" || upstream.contextPrincipal.Subject != p.Subject || upstream.contextPrincipal.IntegrationID != p.IntegrationID {
				t.Fatalf("timed finish lost owner delegation or machine context: actor=%+v principal=%+v", upstream.actor, upstream.contextPrincipal)
			}
			if len(audit.audits) != 2 {
				t.Fatalf("missing SDK audit pair: %d", len(audit.audits))
			}
			for _, event := range audit.audits {
				if event.Principal.Subject != p.Subject || event.Principal.IntegrationID != p.IntegrationID {
					t.Fatalf("audit principal was replaced: %+v", event.Principal)
				}
			}
			if machine && access.permission != "write" {
				t.Fatal("timed finish bypassed write grant")
			}
		})
	}
}

func TestGrantedTrackingSDKTimedFinishDeniesGrantBeforeUpstream(t *testing.T) {
	p := trackingPrincipal("experiments:read", "experiments:write")
	p.IntegrationID = strings.Repeat("a", 32)
	p.Subject = "integration:" + p.IntegrationID
	runID, expID := strings.Repeat("b", 32), strings.Repeat("c", 32)
	upstream := &grantedTimedTrackingFake{fakeMLflowTrackingService: &fakeMLflowTrackingService{}}
	access := &grantedFinishAccessFake{denyGrant: true}
	service := NewGrantedMLflowTracking(upstream, integrationTrackingRecordsFake{run: mlflowtracking.Run{ID: runID, ExperimentID: expID}}, access, []byte(strings.Repeat("k", 32)))
	audit := newFakeMLflowDashboardStore()
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"run_id":%q,"status":"FINISHED","end_time":1789187654321}`, runID)
	grantedSDKTestRouter(p, service, audit).ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/update", strings.NewReader(body)))
	if response.Code != 404 || access.permission != "write" || upstream.finishCalls != 0 || upstream.runID != "" {
		t.Fatalf("grant denial was lost or reached upstream: status=%d permission=%s calls=%d", response.Code, access.permission, upstream.finishCalls)
	}
	if len(audit.audits) != 2 || audit.audits[1].Principal.Subject != p.Subject {
		t.Fatal("denied machine operation lost its audit identity")
	}
}
