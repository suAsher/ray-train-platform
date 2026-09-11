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
	"ray-train-platform-backend/observability"
)

const integrationAPIPath = "/api/v1/jobs/job-01/mlflow/runs/0123456789abcdef0123456789abcdef"
const validIntegrationBatch = `{"metrics":[{"key":"loss","value":0.5,"timestamp":1000,"step":1}],"params":[{"key":"epochs","value":"5"}],"tags":[{"key":"review","value":"candidate"}]}`

type fakeIntegrationProvider struct {
	fakeExperimentProvider
	reads, writes int
	err error
	audit *fakeMLflowDashboardStore
}
func (p *fakeIntegrationProvider) QueryJobRun(ctx context.Context, tenant, job, owner, run string) (observability.JobExperiment, error) {
	p.reads++
	if tenant != "team-a" || job != "job-01" || owner != "user-a" || run != "0123456789abcdef0123456789abcdef" { return observability.JobExperiment{}, errors.New("incorrect identity") }
	if _, ok := ctx.Deadline(); !ok { return observability.JobExperiment{}, errors.New("missing total timeout") }
	return observability.JobExperiment{Run: &observability.ExperimentRun{ID: run}}, p.err
}
func (p *fakeIntegrationProvider) LogJobRunBatch(ctx context.Context, tenant, job, owner, run string, batch observability.MLflowLogBatch) error {
	p.writes++
	if p.audit == nil || len(p.audit.audits) == 0 { return errors.New("write was not audited first") }
	if tenant != "team-a" || job != "job-01" || owner != "user-a" || run != "0123456789abcdef0123456789abcdef" { return errors.New("incorrect identity") }
	if _, ok := ctx.Deadline(); !ok { return errors.New("missing total timeout") }
	return p.err
}

func integrationPrincipal() auth.Principal {
	return auth.Principal{Subject: "user-a", TenantID: "team-a", AuthType: auth.AuthTypePAT, Roles: []string{domain.RoleEngineer}, Scopes: []string{domain.PATScopeJobsRead, "mlflow:write"}}
}
func integrationRouter(p auth.Principal, provider *fakeIntegrationProvider, audit *fakeMLflowDashboardStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	provider.audit = audit
	h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{{ID:"job-01", TenantID:"team-a", UserID:"user-a"}}}, Options{Experiments:provider, MLflowDashboardStore:audit})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	h.RegisterTrainingRoutes(r.Group("/api/v1"))
	return r
}
func integrationRequest(r *gin.Engine, method, body string) *httptest.ResponseRecorder {
	path := integrationAPIPath
	if method == "POST" { path += "/log-batch" }
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	return resp
}

func TestMLflowIntegrationAPIAuthorization(t *testing.T) {
	for _, tc := range []struct{name, method string; alter func(*auth.Principal); status int}{
		{"read owner", "GET", func(p *auth.Principal){}, 200},
		{"read admin", "GET", func(p *auth.Principal){p.Subject="admin"; p.Roles=[]string{domain.RoleTenantAdmin}}, 200},
		{"read peer denied", "GET", func(p *auth.Principal){p.Subject="peer"}, 403},
		{"read scope denied", "GET", func(p *auth.Principal){p.Scopes=[]string{"mlflow:write"}}, 403},
		{"write owner", "POST", func(p *auth.Principal){}, 200},
		{"write old token denied", "POST", func(p *auth.Principal){p.Scopes=[]string{domain.PATScopeJobsRead,domain.PATScopeJobsWrite}}, 403},
		{"write without read denied", "POST", func(p *auth.Principal){p.Scopes=[]string{"mlflow:write"}}, 403},
		{"write browser denied", "POST", func(p *auth.Principal){p.AuthType=auth.AuthTypeLocal}, 403},
		{"write admin denied", "POST", func(p *auth.Principal){p.Subject="admin";p.Roles=[]string{domain.RoleTenantAdmin}}, 403},
		{"write other tenant denied", "POST", func(p *auth.Principal){p.TenantID="team-b"}, 404},
		{"write superadmin other tenant denied", "POST", func(p *auth.Principal){p.TenantID="team-b";p.Roles=[]string{domain.RoleSuperAdmin}}, 404},
	} {
		t.Run(tc.name,func(t *testing.T){
			p:=integrationPrincipal(); tc.alter(&p)
			provider:= &fakeIntegrationProvider{}
			resp:=integrationRequest(integrationRouter(p,provider,newFakeMLflowDashboardStore()),tc.method,validIntegrationBatch)
			if resp.Code!=tc.status {t.Fatalf("status=%d body=%s",resp.Code,resp.Body.String())}
			if tc.status!=200 && provider.reads+provider.writes!=0 {t.Fatal("denied request reached provider")}
		})
	}
}

func TestMLflowIntegrationAPIAuditFailureBlocksWrite(t *testing.T) {
	audit:=newFakeMLflowDashboardStore();audit.auditErr=errors.New("db unavailable")
	provider:= &fakeIntegrationProvider{}
	resp:=integrationRequest(integrationRouter(integrationPrincipal(),provider,audit),"POST",validIntegrationBatch)
	if resp.Code!=503 || provider.writes!=0 {t.Fatalf("unaudited write: %d writes=%d",resp.Code,provider.writes)}
}

func TestMLflowIntegrationAPIBatchValidation(t *testing.T) {
	for _, body:=range []string{
		`{}`, `null`, `{"run_id":"forged"}`, validIntegrationBatch+`{}`,
		`{"metrics":[{"key":"loss","value":1e999,"timestamp":1000,"step":1}]}`,
		`{"metrics":[{"key":"loss","value":null,"timestamp":1000,"step":1}]}`,
		`{"metrics":[{"key":"loss","timestamp":1000,"step":1}]}`,
		`{"metrics":[{"key":"loss","value":1,"timestamp":1000}]}`,
		`{"metrics":[{"key":"loss","value":1,"step":1}]}`,
		`{"metrics":[{"key":"loss","value":1,"timestamp":null,"step":1}]}`,
		`{"metrics":[{"key":"loss","value":1,"timestamp":1000,"step":null}]}`,
		`{"metrics":[{"key":"loss","value":1,"value":2,"timestamp":1000,"step":1}]}`,
		`{"params":[{"key":"x"}]}`,
		`{"tags":[{"key":"x","value":null}]}`,
		`{"params":[],"params":[{"key":"x","value":"v"}]}`,
		`{"params":[],"Params":[{"key":"x","value":"v"}]}`,
		`{"metrics":[{"key":"loss","value":1,"timestamp":1000,"step":-1}]}`,
		`{"params":[{"key":"x","value":"1"},{"key":"x","value":"2"}]}`,
		`{"tags":[{"key":"x","value":"1"},{"key":"x","value":"2"}]}`,
		`{"tags":[{"key":"platform.job_id","value":"forged"}]}`,
		`{"params":[{"key":"mlflow.runName","value":"forged"}]}`,
		`{"tags":[{"key":"run_id","value":"forged"}]}`,
		`{"params":[{"key":"x","value":"`+strings.Repeat("a",270000)+`"}]}`,
	} {
		provider:= &fakeIntegrationProvider{}
		resp:=integrationRequest(integrationRouter(integrationPrincipal(),provider,newFakeMLflowDashboardStore()),"POST",body)
		if resp.Code!=400 && resp.Code!=413 {t.Fatalf("bad body accepted: %d %s",resp.Code,resp.Body.String())}
		if provider.writes!=0 {t.Fatal("bad body forwarded")}
	}
}

func TestMLflowIntegrationAPIRateLimitAndSafeErrors(t *testing.T) {
	provider:= &fakeIntegrationProvider{err:errors.New("s3://private secret upstream body")}
	r:=integrationRouter(integrationPrincipal(),provider,newFakeMLflowDashboardStore())
	limited:=false
	for i:=0;i<121;i++ {
		resp:=integrationRequest(r,"POST",validIntegrationBatch)
		if strings.Contains(resp.Body.String(),"s3://") {t.Fatal("upstream error leaked")}
		if resp.Code==429 {limited=true;break}
		if resp.Code!=502 {t.Fatalf("unexpected error: %d %s",resp.Code,resp.Body.String())}
	}
	if !limited {t.Fatal("no write rate limit")}
}

func TestMLflowIntegrationAPITerminalRunConflict(t *testing.T) {
	p:= &fakeIntegrationProvider{err:observability.ErrMLflowRunNotRunning}
	resp:=integrationRequest(integrationRouter(integrationPrincipal(),p,newFakeMLflowDashboardStore()),http.MethodPost,validIntegrationBatch)
	if resp.Code!=409 {t.Fatalf("want conflict: %d %s",resp.Code,resp.Body.String())}
}

func TestMLflowIntegrationAPIBatchConflict(t *testing.T) {
	p:= &fakeIntegrationProvider{err:observability.ErrMLflowBatchConflict}
	resp:=integrationRequest(integrationRouter(integrationPrincipal(),p,newFakeMLflowDashboardStore()),http.MethodPost,validIntegrationBatch)
	if resp.Code!=409||!strings.Contains(resp.Body.String(),"MLFLOW_BATCH_CONFLICT") {t.Fatalf("want batch conflict: %d %s",resp.Code,resp.Body.String())}
}

func TestMLflowIntegrationAPIExplicitZeroesAndEmptyValuesAreValid(t *testing.T) {
	body:=`{"metrics":[{"key":"loss","value":0,"timestamp":0,"step":0}],"params":[{"key":"optional","value":""}]}`
	resp:=integrationRequest(integrationRouter(integrationPrincipal(),&fakeIntegrationProvider{},newFakeMLflowDashboardStore()),http.MethodPost,body)
	if resp.Code!=200 {t.Fatalf("explicit zeroes rejected: %d %s",resp.Code,resp.Body.String())}
}

func TestMLflowIntegrationAPIRejectsOversizedCollections(t *testing.T) {
	for _, field:=range []string{"metrics","params","tags"} {
		items:=make([]map[string]any,101)
		for i:=range items {
			items[i]=map[string]any{"key":"x","value":"v"}
			if field=="metrics" {items[i]=map[string]any{"key":"loss","value":1,"timestamp":1000,"step":i}}
		}
		body,_:=json.Marshal(map[string]any{field:items})
		provider:= &fakeIntegrationProvider{}
		resp:=integrationRequest(integrationRouter(integrationPrincipal(),provider,newFakeMLflowDashboardStore()),"POST",string(body))
		if resp.Code!=400 || provider.writes!=0 {t.Fatalf("oversized %s allowed: %d",field,resp.Code)}
	}
}

func TestMLflowIntegrationAPIAuditContainsOnlyMetadata(t *testing.T) {
	audit:=newFakeMLflowDashboardStore()
	resp:=integrationRequest(integrationRouter(integrationPrincipal(),&fakeIntegrationProvider{},audit),"POST",validIntegrationBatch)
	if resp.Code!=200 || len(audit.audits)!=2 {t.Fatalf("missing audits: %d %+v",resp.Code,audit.audits)}
	if audit.audits[0].Status!=102 || audit.audits[1].Status!=200 || audit.audits[1].Principal.AuthType!=auth.AuthTypePAT {t.Fatalf("incorrect audit stages: %+v",audit.audits)}
	encoded,_:=json.Marshal(audit.audits)
	if strings.Contains(string(encoded),"candidate") || strings.Contains(string(encoded),"epochs") {t.Fatal("audit contains payload")}
}
