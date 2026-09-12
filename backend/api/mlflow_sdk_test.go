package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/mlflowtracking"
)

func (s *fakeMLflowTrackingService) FinishRunAt(ctx context.Context, actor mlflowtracking.Actor, id, status string, endTimeMS int64) (mlflowtracking.Run, error) {
	run, err := s.FinishRun(ctx, actor, id, status)
	run.EndTimeMS = endTimeMS
	return run, err
}

func sdkTestRouter(principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if principal.Subject != "" {
			c.Set("ray-platform-principal", principal)
		}
		c.Next()
	})
	h := NewHandler(&fakeJobRepository{}, Options{})
	h.RegisterMLflowSDKRoutes(r.Group("/api/v1"))
	return r
}

func sdkServiceRouter(p auth.Principal, service *fakeMLflowTrackingService, audit *fakeMLflowDashboardStore) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	h := NewHandler(&fakeJobRepository{}, Options{MLflowTracking: service, MLflowDashboardStore: audit})
	h.RegisterMLflowSDKRoutes(r.Group("/api/v1"))
	return r
}

func TestMLflowSDKNativeWriteContractAndAuthorization(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct{ route, body string }{
		{"log-batch", `{"params":[{"key":"epochs","value":"3"}]}`},
		{"log-parameter", `{"key":"epochs","value":"3"}`},
		{"set-tag", `{"key":"review","value":"candidate"}`},
		{"log-metric", `{"key":"loss","value":0.5,"timestamp":1000,"step":1}`},
		{"update", `{"status":"FINISHED","end_time":2000}`},
	} {
		t.Run(tc.route, func(t *testing.T) {
			for _, allowed := range []bool{false, true} {
				p := trackingPrincipal("experiments:read")
				if allowed {
					p.Scopes = append(p.Scopes, "experiments:write")
				}
				service := &fakeMLflowTrackingService{run: mlflowtracking.Run{ID: id, State: "FINISHED"}}
				audit := newFakeMLflowDashboardStore()
				r := sdkServiceRouter(p, service, audit)
				w := httptest.NewRecorder()
				body := `{"run_id":"` + id + `",` + tc.body[1:]
				r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/"+tc.route, strings.NewReader(body)))
				if !allowed {
					if w.Code != 403 || service.runID != "" {
						t.Fatalf("scope bypass: %d", w.Code)
					}
					continue
				}
				if w.Code != 200 || service.runID != id || service.actor.UserID != p.Subject || service.actor.TenantID != p.TenantID || len(audit.audits) != 2 {
					t.Fatalf("mapping/audit mismatch: %d %s %+v", w.Code, w.Body.String(), service)
				}
				if strings.Contains(w.Body.String(), `"success"`) {
					t.Fatal("platform envelope leaked into SDK response")
				}
			}
		})
	}
}

func TestMLflowSDKServiceDenialAndAuditFailure(t *testing.T) {
	for _, auditFailure := range []bool{false, true} {
		service := &fakeMLflowTrackingService{err: mlflowtracking.ErrNotFound}
		audit := newFakeMLflowDashboardStore()
		if auditFailure {
			audit.auditErr = mlflowtracking.ErrUnavailable
		}
		p := trackingPrincipal("experiments:read", "experiments:write")
		r := sdkServiceRouter(p, service, audit)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/log-parameter", strings.NewReader(`{"run_id":"0123456789abcdef0123456789abcdef","key":"epochs","value":"3"}`)))
		if auditFailure {
			if w.Code != 503 || service.runID != "" {
				t.Fatal("unaudited mutation")
			}
		} else if w.Code != 404 {
			t.Fatalf("ownership error lost: %d", w.Code)
		}
	}
}

func TestMLflowSDKRejectsInvalidBatchSemantics(t *testing.T) {
	for _, body := range []string{
		`{"key":"platform.job_id","value":"forged"}`,
		`{"key":"mlflow.runName","value":"forged"}`,
		`{"key":"epochs","value":null}`,
		`{"key":"epochs","value":"3","timestamp":0}`,
		`{"key":"epochs","value":"3","status":"RUNNING"}`,
	} {
		var req mlflowSDKWriteRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if _, err := req.logBatch("log-parameter"); err == nil {
			t.Fatalf("bad semantic body accepted %s", body)
		}
	}
}

func TestMLflowSDKGetPreservesVirtualIDAndRejectsTransitionalStatus(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, state := range []string{"RUNNING", "FINISHED", "FINISHING", "PENDING"} {
		service := &fakeMLflowTrackingService{detail: mlflowtracking.RunDetail{Run: mlflowtracking.Run{ID: id, UpstreamID: strings.Repeat("f", 32), ExperimentID: strings.Repeat("a", 32), State: state}, Latest: map[string]float64{"loss": 0.5}, Params: map[string]string{"epochs": "3"}}}
		w := httptest.NewRecorder()
		sdkServiceRouter(trackingPrincipal("experiments:read"), service, newFakeMLflowDashboardStore()).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/get?run_id="+id, nil))
		if state == "FINISHING" || state == "PENDING" {
			if w.Code != 409 {
				t.Fatalf("illegal SDK status %s: %s", state, w.Body.String())
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"run_id":"`+id+`"`) || strings.Contains(w.Body.String(), strings.Repeat("f", 32)) {
			t.Fatalf("native result mismatch: %s", w.Body.String())
		}
	}
}

func TestMLflowSDKRateAndInputLimits(t *testing.T) {
	service:=&fakeMLflowTrackingService{err:mlflowtracking.ErrBusy}
	r:=sdkServiceRouter(trackingPrincipal("experiments:read","experiments:write"),service,newFakeMLflowDashboardStore())
	for _,payload:=range []string{
		`{"run_id":"0123456789abcdef0123456789abcdef","key":"loss","value":null,"timestamp":1,"step":1}`,
		`{"run_id":"0123456789abcdef0123456789abcdef","key":"loss","value":1e999,"timestamp":1,"step":1}`,
		`{"run_id":"0123456789abcdef0123456789abcdef","key":"loss","value":0.5,"timestamp":-1,"step":1}`,
		`{"run_id":"0123456789abcdef0123456789abcdef","key":"loss","value":0.5,"timestamp":1,"step":-1}`,
		strings.Repeat(" ",256*1024+1),
	} {
		w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("POST","/api/v1/mlflow-tracking/api/2.0/mlflow/runs/log-metric",strings.NewReader(payload)))
		if w.Code!=400||service.runID!="" {t.Fatalf("bad metric reached service: %d",w.Code)}
	}
	limited:=false
	for i:=0;i<130;i++ {
		w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("GET","/api/v1/mlflow-tracking/api/2.0/mlflow/runs/get?run_id=0123456789abcdef0123456789abcdef",nil))
		if w.Code==429 {limited=true;if w.Header().Get("Retry-After")=="" {t.Fatal("rate limit lacks retry guidance")};break}
		if w.Code!=409 {t.Fatalf("busy status: %d",w.Code)}
	}
	if !limited {t.Fatal("SDK read rate not bounded")}
}

func TestMLflowSDKRequiresExplicitScopesAndPAT(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal auth.Principal
		status    int
	}{
		{"anonymous", auth.Principal{}, 401},
		{"browser", auth.Principal{Subject: "owner", TenantID: "team", AuthType: auth.AuthTypeLocal}, 403},
		{"legacy PAT", integrationPrincipal(), 403},
		{"read PAT missing service", auth.Principal{Subject: "owner", TenantID: "team", AuthType: auth.AuthTypePAT, Scopes: []string{"experiments:read"}}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			sdkTestRouter(tc.principal).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/mlflow-tracking/api/2.0/mlflow/runs/get?run_id=0123456789abcdef0123456789abcdef", nil))
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body["error_code"] == nil || body["success"] != nil {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMLflowSDKDoesNotExposeArbitraryProxy(t *testing.T) {
	p := auth.Principal{Subject: "owner", TenantID: "team", AuthType: auth.AuthTypePAT, Scopes: []string{"experiments:read", "experiments:write"}}
	for _, path := range []string{"runs/create", "runs/delete", "registered-models/create", "model-versions/transition-stage", "logged-models/create", "artifacts/list", "traces/search"} {
		w := httptest.NewRecorder()
		sdkTestRouter(p).ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mlflow-tracking/api/2.0/mlflow/"+path, strings.NewReader(`{}`)))
		if w.Code != 404 {
			t.Fatalf("unlisted endpoint %s status %d", path, w.Code)
		}
	}
}

func TestMLflowSDKStrictPayloadDecoder(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"run_id":"a","run_id":"b"}`, `{"Run_ID":"a"}`, `{"run_id":"a","unknown":1}`, `{"run_id":"a"} {}`, `{"run_id":"a","params":[{"key":"x","value":"a","value":"b"}]}`} {
		var req mlflowSDKWriteRequest
		if err := decodeMLflowSDKBody(strings.NewReader(body), &req); err == nil {
			t.Fatalf("ambiguous payload accepted: %s", body)
		}
	}
	var req mlflowSDKWriteRequest
	if err := decodeMLflowSDKBody(strings.NewReader(`{"run_id":"0123456789abcdef0123456789abcdef","params":[{"key":"epochs","value":"5"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	if err := decodeMLflowSDKBody(strings.NewReader(`{"run_id":"0123456789abcdef0123456789abcdef","run_uuid":"0123456789abcdef0123456789abcdef","key":"epochs","value":"5"}`), &req); err != nil {
		t.Fatal(err)
	}
	if err := decodeMLflowSDKBody(strings.NewReader(`{"run_id":"0123456789abcdef0123456789abcdef","run_uuid":"ffffffffffffffffffffffffffffffff","key":"epochs","value":"5"}`), &req); err == nil {
		t.Fatal("conflicting SDK run_uuid accepted")
	}
}
