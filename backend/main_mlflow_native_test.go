package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type nativeMLflowAuditStore struct{ events []repositories.MLflowAuditEvent }

func (*nativeMLflowAuditStore) CreateMLflowDashboardTicket(context.Context, repositories.MLflowDashboardTicketRecord) error {
	return nil
}
func (*nativeMLflowAuditStore) ConsumeMLflowDashboardTicket(context.Context, string, time.Time) (repositories.MLflowDashboardTicketRecord, error) {
	return repositories.MLflowDashboardTicketRecord{}, nil
}
func (*nativeMLflowAuditStore) AuthorizeMLflowDashboardPrincipal(context.Context, auth.Principal) (bool, error) {
	return true, nil
}
func (s *nativeMLflowAuditStore) CreateMLflowAuditLog(_ context.Context, event repositories.MLflowAuditEvent) error {
	s.events = append(s.events, event)
	return nil
}

func nativeMLflowMainRouter(target string, pat auth.PATVerifier, local auth.LocalSessionVerifier, audit *nativeMLflowAuditStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := api.NewHandler(&mainJobRepository{}, api.Options{MLflowDashboardEnabled: true, MLflowTrackingURL: target, MLflowDashboardStore: audit})
	r := gin.New()
	registerAPIRoutesWithLocalAuth(r, h, nil, nil, nil, nil, nil, pat, local, nil, nil, config.Config{OIDCRequired: true})
	return r
}

func nativeMLflowPAT(scopes ...string) *mainDatasetPATVerifier {
	return &mainDatasetPATVerifier{principal: auth.Principal{Subject: "user-a", TenantID: "team-a", Username: "member-a"}, scopes: scopes}
}

func TestNativeMLflowRegisteredRoutesShareUpstreamExperimentsRunsRegistryAndArtifacts(t *testing.T) {
	for _, tc := range []struct{ method, path, body, response string }{
		{"POST", "/api/2.0/mlflow/experiments/search", `{"max_results":100}`, `{"experiments":[{"experiment_id":"7","name":"another-members-experiment"}]}`},
		{"POST", "/api/2.0/mlflow/runs/create", `{"experiment_id":"7"}`, `{"run":{"info":{"run_id":"abc","artifact_uri":"mlflow-artifacts:/7/abc/artifacts"}}}`},
		{"POST", "/api/2.0/mlflow/registered-models/create", `{"name":"shared-model"}`, `{"registered_model":{"name":"shared-model"}}`},
		{"PUT", "/api/2.0/mlflow-artifacts/artifacts/7/abc/artifacts/model.bin", "\x00\x01checkpoint\xff", "{}"},
		{"GET", "/api/2.0/mlflow-artifacts/artifacts/7/abc/artifacts/model.bin", "", "\x00\x01checkpoint\xff"},
		{"POST", "/api/3.0/mlflow/traces/search", `{}`, `{"traces":[]}`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != tc.method || r.URL.Path != "/mlflow"+tc.path || string(body) != tc.body {
					t.Errorf("changed native request method=%s path=%s body=%q", r.Method, r.URL.Path, body)
				}
				for _, key := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Auth-Request-Access-Token", "X-Api-Key"} {
					if r.Header.Get(key) != "" {
						t.Errorf("credential header forwarded: %s", key)
					}
				}
				w.Header().Set("Set-Cookie", "upstream=private")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer upstream.Close()
			audit := &nativeMLflowAuditStore{}
			r := nativeMLflowMainRouter(upstream.URL+"/mlflow", nativeMLflowPAT("mlflow:full"), nil, audit)
			req := httptest.NewRequest(tc.method, "/api/v1/mlflow-native"+tc.path+"?max_results=100", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer rpt_test-native")
			for _, key := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Auth-Request-Access-Token", "X-Api-Key"} {
				req.Header.Set(key, "private")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 200 || w.Body.String() != tc.response || calls != 1 {
				t.Fatalf("native result: status=%d calls=%d body=%q", w.Code, calls, w.Body.String())
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unsafe native response headers")
			}
			if len(audit.events) == 0 {
				t.Fatal("native access was not audited")
			}
			for _, event := range audit.events {
				if event.Principal.Subject != "user-a" || event.Principal.AuthType != auth.AuthTypePAT || strings.Contains(event.Path, "?") {
					t.Fatalf("incorrect audit principal/path: %+v", event)
				}
			}
		})
	}
}

func TestNativeMLflowRegisteredRoutesRequireExplicitPersonalScope(t *testing.T) {
	for _, tc := range []struct{ name string; pat *mainDatasetPATVerifier; bearer string; want int }{
		{"anonymous", nil, "", 401},
		{"ordinary PAT", nativeMLflowPAT("experiments:read", "experiments:write", "mlflow:write"), "rpt_ordinary", 403},
		{"cookie session", nil, "rls_session", 403},
		{"integration", &mainDatasetPATVerifier{principal: auth.Principal{Subject: "integration:id", TenantID: "team-a", IntegrationID: "id"}, scopes: []string{"mlflow:full"}}, "rpt_integration", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unauthorized upstream access") }))
			defer upstream.Close()
			var verifier auth.PATVerifier
			if tc.pat != nil {
				verifier = tc.pat
			}
			r := nativeMLflowMainRouter(upstream.URL+"/mlflow", verifier, &mainDatasetLocalVerifier{principal: auth.Principal{Subject: "user-a", TenantID: "team-a"}}, &nativeMLflowAuditStore{})
			req := httptest.NewRequest("POST", "/api/v1/mlflow-native/api/2.0/mlflow/runs/create", strings.NewReader(`{}`))
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestNativeMLflowFullScopeIsExplicitAndDoesNotImplyPlatformPermissions(t *testing.T) {
	scopes, err := domain.NormalizePATScopes([]string{"mlflow:full"})
	if err != nil || len(scopes) != 1 || scopes[0] != "mlflow:full" {
		t.Fatalf("explicit native scope: %v %v", scopes, err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mlflow/api/2.0/mlflow/experiments/search" {
			t.Fatalf("unexpected native upstream path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"experiments":[]}`)
	}))
	defer upstream.Close()
	r := nativeMLflowMainRouter(upstream.URL+"/mlflow", nativeMLflowPAT("mlflow:full"), nil, &nativeMLflowAuditStore{})

	capabilities := httptest.NewRecorder()
	capabilityRequest := httptest.NewRequest("GET", "/api/v1/mlflow/capabilities", nil)
	capabilityRequest.Header.Set("Authorization", "Bearer rpt_native")
	r.ServeHTTP(capabilities, capabilityRequest)
	if capabilities.Code != http.StatusOK {
		t.Fatalf("full scope could not discover capabilities: %d %s", capabilities.Code, capabilities.Body.String())
	}
	var envelope struct {
		Data struct {
			Read                bool   `json:"read"`
			Write               bool   `json:"write"`
			NativeAvailable     bool   `json:"nativeAvailable"`
			NativeBasePath      string `json:"nativeBasePath"`
			NativeClientVersion string `json:"nativeClientVersion"`
		} `json:"data"`
	}
	if err := json.Unmarshal(capabilities.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Read || envelope.Data.Write || !envelope.Data.NativeAvailable || envelope.Data.NativeBasePath != "/api/v1/mlflow-native" || envelope.Data.NativeClientVersion != "3.14.0" {
		t.Fatalf("full-only capability contract mismatch: %+v", envelope.Data)
	}

	native := httptest.NewRecorder()
	nativeRequest := httptest.NewRequest("POST", "/api/v1/mlflow-native/api/2.0/mlflow/experiments/search", strings.NewReader(`{}`))
	nativeRequest.Header.Set("Authorization", "Bearer rpt_native")
	r.ServeHTTP(native, nativeRequest)
	if native.Code != http.StatusOK {
		t.Fatalf("full scope native route failed: %d %s", native.Code, native.Body.String())
	}

	scoped := httptest.NewRecorder()
	scopedRequest := httptest.NewRequest("GET", "/api/v1/mlflow/experiments", nil)
	scopedRequest.Header.Set("Authorization", "Bearer rpt_native")
	r.ServeHTTP(scoped, scopedRequest)
	if scoped.Code != http.StatusForbidden {
		t.Fatalf("native scope escalated governed experiments access: %d", scoped.Code)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer rpt_native")
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("native scope escalated platform jobs access: %d", w.Code)
	}
}
