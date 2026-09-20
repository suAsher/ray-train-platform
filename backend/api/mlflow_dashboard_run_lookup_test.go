package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestPublicDashboardRunLinkUsesActualRunInsteadOfViewerTenantCatalog(t *testing.T) {
	const runID = "7d5aa4a3e8e843a1af9d2eeb8173e22f"
	for _, role := range []string{domain.RoleSuperAdmin, domain.RoleEngineer} {
		t.Run(role, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/mlflow/api/2.0/mlflow/runs/get" || r.URL.Query().Get("run_id") != runID { t.Fatalf("unexpected lookup: %s %s", r.Method, r.URL) }
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" { t.Error("leaked browser credentials") }
				fmt.Fprintf(w, `{"run":{"info":{"run_id":"%s","experiment_id":"14"}}}`, runID)
			}))
			defer upstream.Close()
			store := newFakeMLflowDashboardStore()
			h := newMLflowDashboardTestHandler(store, time.Now())
			h.mlflowDashboardPublicEnabled = true
			h.mlflowTrackingURL = upstream.URL + "/mlflow"
			principal := auth.Principal{Subject:"viewer", TenantID:"local", Roles:[]string{role}, AuthType:auth.AuthTypeLocal}
			w := httptest.NewRecorder()
			mlflowAccessRouter(h, &principal, true).ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mlflow-dashboard-access", strings.NewReader(`{"runId":"`+runID+`"}`)))
			if w.Code != 200 { t.Fatalf("shared Run lookup: %d %s", w.Code, w.Body.String()) }
			var payload struct { Data struct { URL string `json:"url"` } `json:"data"` }
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil { t.Fatal(err) }
			if !strings.HasPrefix(payload.Data.URL, "/mlflow/?access_token=") { t.Fatalf("changed Portal contract: %s",payload.Data.URL) }
			exchange := httptest.NewRecorder()
			mlflowProxyRouter(h).ServeHTTP(exchange, httptest.NewRequest("GET", payload.Data.URL, nil))
			if exchange.Code != 302 || exchange.Header().Get("Location") != "/mlflow/#/experiments/14/runs/"+runID { t.Fatalf("incorrect Run redirect: %d %s",exchange.Code,exchange.Header().Get("Location")) }
		})
	}
}

func TestPublicDashboardRunLookupRejectsMissingOrMismatchedRun(t *testing.T) {
	const runID = "7d5aa4a3e8e843a1af9d2eeb8173e22f"
	for _, tc := range []struct{ status int; body string }{
		{404,`{"error_code":"RESOURCE_DOES_NOT_EXIST"}`},
		{200,`{"run":{"info":{"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","experiment_id":"14"}}}`},
		{200,`{"run":{"info":{"run_id":"`+runID+`","experiment_id":"//evil.example"}}}`},
		{200,`not-json`},
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){ w.WriteHeader(tc.status); fmt.Fprint(w,tc.body) }))
		store := newFakeMLflowDashboardStore()
		h := newMLflowDashboardTestHandler(store,time.Now())
		h.mlflowDashboardPublicEnabled = true
		h.mlflowTrackingURL = upstream.URL
		principal := auth.Principal{Subject:"viewer",TenantID:"local",AuthType:auth.AuthTypeLocal}
		w := httptest.NewRecorder()
		mlflowAccessRouter(h,&principal,true).ServeHTTP(w,httptest.NewRequest("POST","/api/v1/mlflow-dashboard-access",strings.NewReader(`{"runId":"`+runID+`"}`)))
		if w.Code != 404 || len(store.created)!=0 { t.Fatalf("unsafe Run redirect: %d tickets=%d",w.Code,len(store.created)) }
		upstream.Close()
	}
}
