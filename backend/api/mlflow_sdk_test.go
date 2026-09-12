package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
)

func sdkTestRouter(principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { if principal.Subject != "" { c.Set("ray-platform-principal", principal) }; c.Next() })
	h := NewHandler(&fakeJobRepository{}, Options{})
	h.RegisterMLflowSDKRoutes(r.Group("/api/v1"))
	return r
}

func TestMLflowSDKRequiresExplicitScopesAndPAT(t *testing.T) {
	for _, tc := range []struct { name string; principal auth.Principal; status int }{
		{"anonymous", auth.Principal{}, 401},
		{"browser", auth.Principal{Subject:"owner",TenantID:"team",AuthType:auth.AuthTypeLocal},403},
		{"legacy PAT", integrationPrincipal(),403},
		{"read PAT missing service", auth.Principal{Subject:"owner",TenantID:"team",AuthType:auth.AuthTypePAT,Scopes:[]string{"experiments:read"}},503},
	} {
		t.Run(tc.name,func(t *testing.T){
			w:=httptest.NewRecorder()
			sdkTestRouter(tc.principal).ServeHTTP(w,httptest.NewRequest("GET","/api/v1/mlflow-tracking/api/2.0/mlflow/runs/get?run_id=0123456789abcdef0123456789abcdef",nil))
			var body map[string]any
			if err:=json.Unmarshal(w.Body.Bytes(),&body);err!=nil {t.Fatal(err)}
			if w.Code!=tc.status || body["error_code"]==nil || body["success"]!=nil {t.Fatalf("status=%d body=%s",w.Code,w.Body.String())}
		})
	}
}

func TestMLflowSDKDoesNotExposeArbitraryProxy(t *testing.T) {
	p:=auth.Principal{Subject:"owner",TenantID:"team",AuthType:auth.AuthTypePAT,Scopes:[]string{"experiments:read","experiments:write"}}
	for _, path:=range []string{"runs/create","runs/delete","registered-models/create","model-versions/transition-stage","logged-models/create","artifacts/list","traces/search"} {
		w:=httptest.NewRecorder()
		sdkTestRouter(p).ServeHTTP(w,httptest.NewRequest("POST","/api/v1/mlflow-tracking/api/2.0/mlflow/"+path,strings.NewReader(`{}`)))
		if w.Code!=404 {t.Fatalf("unlisted endpoint %s status %d",path,w.Code)}
	}
}

func TestMLflowSDKStrictPayloadDecoder(t *testing.T) {
	for _,body:=range []string{`null`,`{}`,`{"run_id":"a","run_id":"b"}`,`{"Run_ID":"a"}`,`{"run_id":"a","unknown":1}`,`{"run_id":"a"} {}`,`{"run_id":"a","params":[{"key":"x","value":"a","value":"b"}]}`} {
		var req mlflowSDKWriteRequest
		if err:=decodeMLflowSDKBody(strings.NewReader(body),&req);err==nil {t.Fatalf("ambiguous payload accepted: %s",body)}
	}
	var req mlflowSDKWriteRequest
	if err:=decodeMLflowSDKBody(strings.NewReader(`{"run_id":"0123456789abcdef0123456789abcdef","params":[{"key":"epochs","value":"5"}]}`),&req);err!=nil {t.Fatal(err)}
}
