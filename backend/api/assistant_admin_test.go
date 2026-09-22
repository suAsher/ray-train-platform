package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/assistantidle"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

func adminAssistantRouter(h *Handler, principal *auth.Principal) *gin.Engine {
	r := gin.New()
	if principal != nil {
		r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", *principal) })
	}
	h.RegisterAdminRoutes(r.Group("/api/v1"))
	return r
}

func TestAssistantAdminRequiresInteractiveSuperAdmin(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		kind       auth.AuthenticationType
		code       int
	}{
		{"anonymous", "", "", 401},
		{"engineer", domain.RoleEngineer, auth.AuthTypeLocal, 403},
		{"tenant admin", domain.RoleTenantAdmin, auth.AuthTypeLocal, 403},
		{"PAT superadmin", domain.RoleSuperAdmin, auth.AuthTypePAT, 403},
		{"local superadmin", domain.RoleSuperAdmin, auth.AuthTypeLocal, 200},
		{"OIDC superadmin", domain.RoleSuperAdmin, auth.AuthTypeOIDC, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p *auth.Principal
			if tc.kind != "" {
				p = &auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{tc.role}, AuthType: tc.kind}
			}
			w := httptest.NewRecorder()
			adminAssistantRouter(NewHandler(&fakeJobRepository{}, Options{}), p).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status", nil))
			if w.Code != tc.code {
				t.Fatalf("status=%d, want %d", w.Code, tc.code)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("admin status must not be cached")
			}
		})
	}
}

func TestAssistantAdminDisabledIsExplicitAndReadOnly(t *testing.T) {
	p := &auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	r := adminAssistantRouter(NewHandler(&fakeJobRepository{}, Options{}), p)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status", nil))
	if w.Code != 200 {
		t.Fatalf("disabled admin status=%d", w.Code)
	}
	var response struct {
		Data struct {
			Enabled  bool `json:"enabled"`
			ReadOnly bool `json:"readOnly"`
			Idle     struct {
				Configured           bool   `json:"configured"`
				ObservationAvailable bool   `json:"observationAvailable"`
				Reason               string `json:"reason"`
			} `json:"idle"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Enabled || !response.Data.ReadOnly || response.Data.Idle.Configured || response.Data.Idle.ObservationAvailable || response.Data.Idle.Reason != "not_configured" {
		t.Fatalf("unexpected disabled state: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/admin/status", nil))
	if w.Code != 404 && w.Code != 405 {
		t.Fatal("status exposed a mutation method")
	}
}

type assistantStatusRoundTrip func(*http.Request) (*http.Response, error)

func (f assistantStatusRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type assistantStatusSourceFunc func(context.Context, string) (k8s.AssistantIdleStatus, error)

func (f assistantStatusSourceFunc) ObserveAssistantStatus(c context.Context, n string) (k8s.AssistantIdleStatus, error) {
	return f(c, n)
}
func TestAssistantAdminControllerBoundsAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Minute)
	valid := assistantidle.ControllerStatus{Enabled: true, State: assistantidle.StateReady, Reason: "service_ready", ObservedAt: &now, Gate: assistantidle.GateStatus{Allow: true, Epoch: "epoch", ValidUntil: now.Add(3 * time.Second)}}
	for _, tc := range []struct {
		name   string
		modify func(*assistantidle.ControllerStatus)
		raw    string
		code   int
		want   string
	}{
		{name: "fresh", code: 200, want: "observed"},
		{name: "stale", modify: func(s *assistantidle.ControllerStatus) { s.ObservedAt = &old }, code: 200, want: "controller_stale"},
		{name: "closed gate", modify: func(s *assistantidle.ControllerStatus) { s.Gate.Allow = false }, code: 200, want: "controller_stale"},
		{name: "unsafe reason", modify: func(s *assistantidle.ControllerStatus) { s.Reason = "secret-url" }, code: 200, want: "controller_unavailable"},
		{name: "oversize", raw: strings.Repeat("x", 4097), code: 200, want: "controller_unavailable"},
		{name: "redirect", code: 302, want: "controller_unavailable"},
		{name: "invalid body", raw: "secret-invalid", code: 200, want: "controller_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			if tc.modify != nil {
				tc.modify(&s)
			}
			raw, _ := json.Marshal(s)
			if tc.raw != "" {
				raw = []byte(tc.raw)
			}
			h := NewHandler(&fakeJobRepository{}, Options{})
			h.assistantStatusHTTP = &http.Client{Transport: assistantStatusRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "http://assistant-idle-controller.raytrain-assistant-test.svc.cluster.local:8080/status" || r.Method != "GET" || r.Header.Get("Authorization") != "" {
					t.Fatal("unsafe status request")
				}
				return &http.Response{StatusCode: tc.code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			got, reason := h.readAssistantControllerStatus(context.Background(), "raytrain-assistant-test")
			if reason != tc.want || (tc.want != "observed" && got.Gate.Allow) {
				t.Fatalf("reason=%s status=%+v", reason, got)
			}
		})
	}
}
func TestAssistantAdminControllerHTTPRejectsProxyRedirectAndCancellation(t *testing.T) {
	client := newAssistantStatusHTTPClient()
	if client.Transport.(*http.Transport).Proxy != nil || client.Timeout > time.Second {
		t.Fatal("unsafe transport")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); err != http.ErrUseLastResponse {
		t.Fatal("redirect enabled")
	}
	h := NewHandler(&fakeJobRepository{}, Options{})
	h.assistantStatusHTTP = &http.Client{Transport: assistantStatusRoundTrip(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, reason := h.readAssistantControllerStatus(ctx, "raytrain-assistant-test"); reason != "controller_unavailable" || got.Gate.Allow {
		t.Fatal("cancel must close observed readiness")
	}
}
func TestAssistantAdminDiscardsFailedObservationAndRejectsQuery(t *testing.T) {
	p := &auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	h := NewHandler(&fakeJobRepository{}, Options{AssistantIdleNamespace: "raytrain-assistant-test", AssistantStatusSource: assistantStatusSourceFunc(func(context.Context, string) (k8s.AssistantIdleStatus, error) {
		return k8s.AssistantIdleStatus{InferenceReady: true, Pods: []k8s.AssistantPodStatus{{Name: "stale-pod"}}}, errors.New("secret-provider-url")
	})})
	r := adminAssistantRouter(h, p)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "stale-pod") || strings.Contains(w.Body.String(), "secret-provider-url") || !strings.Contains(w.Body.String(), `"inferenceReady":false`) {
		t.Fatalf("unsafe failed observation: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status?namespace=default", nil))
	if w.Code != 400 {
		t.Fatal("request may override fixed scope")
	}
	h.assistantStatusSlots <- struct{}{}
	h.assistantStatusSlots <- struct{}{}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status", nil))
	if w.Code != 429 {
		t.Fatal("unbounded concurrent observation")
	}
}

func TestAssistantAdminReadinessRequiresEveryFreshSignal(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		enabled, rayReady, suspended, deploymentReady, gate bool
		wantObservation, wantInference                      bool
	}{
		{"ready", true, true, false, true, true, true, true},
		{"disabled", false, false, false, true, false, true, false},
		{"ray not ready", true, false, false, true, true, false, false},
		{"suspended", true, true, true, true, true, false, false},
		{"deployment not ready", true, true, false, false, true, false, false},
		{"gate closed", true, true, false, true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := "raytrain-assistant-test"
			now := time.Now().UTC()
			snapshot := k8s.EmptyAssistantStatus(ns)
			snapshot.Enabled = &tc.enabled
			snapshot.RayService.Ready = tc.rayReady
			snapshot.RayService.Suspended = tc.suspended
			snapshot.Deployments = []k8s.AssistantDeploymentStatus{{Name: "assistant-idle-controller", Desired: 1, Ready: 1, Available: 1}, {Name: "assistant-idle-reaper", Desired: 1, Ready: 1, Available: 1}}
			if !tc.deploymentReady {
				snapshot.Deployments[1].Ready = 0
			}
			status := assistantidle.ControllerStatus{Enabled: tc.enabled, State: assistantidle.StateReady, Reason: "service_ready", ObservedAt: &now, Gate: assistantidle.GateStatus{Allow: tc.gate, Epoch: "epoch", ValidUntil: now.Add(3 * time.Second)}}
			raw, _ := json.Marshal(status)
			h := NewHandler(&fakeJobRepository{}, Options{AssistantIdleNamespace: ns, AssistantStatusSource: assistantStatusSourceFunc(func(ctx context.Context, n string) (k8s.AssistantIdleStatus, error) {
				if n != ns {
					t.Fatal("wrong namespace")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded observation")
				}
				return snapshot, nil
			})})
			h.assistantStatusHTTP = &http.Client{Transport: assistantStatusRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			p := &auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
			w := httptest.NewRecorder()
			adminAssistantRouter(h, p).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status", nil))
			var response struct {
				Data assistantAdminResponse `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || response.Data.Idle.ObservationAvailable != tc.wantObservation || response.Data.Idle.InferenceReady != tc.wantInference {
				t.Fatalf("wrong aggregate: %s", w.Body.String())
			}
		})
	}
}

func TestAssistantAdminStoppedControllerPreservesObservedZeroGPU(t *testing.T) {
	enabled := true
	ns := "raytrain-assistant-test"
	snapshot := k8s.EmptyAssistantStatus(ns)
	snapshot.Enabled = &enabled
	snapshot.Deployments = []k8s.AssistantDeploymentStatus{{Name: "assistant-idle-controller", Desired: 0}, {Name: "assistant-idle-reaper", Desired: 0}}
	h := NewHandler(&fakeJobRepository{}, Options{AssistantIdleNamespace: ns, AssistantStatusSource: assistantStatusSourceFunc(func(context.Context, string) (k8s.AssistantIdleStatus, error) { return snapshot, nil })})
	h.assistantStatusHTTP = &http.Client{Transport: assistantStatusRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("stopped controller must not be contacted")
		return nil, errors.New("unexpected HTTP")
	})}
	p := &auth.Principal{Subject: "admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	w := httptest.NewRecorder()
	adminAssistantRouter(h, p).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/admin/status", nil))
	var response struct {
		Data assistantAdminResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Data.Idle
	if w.Code != 200 || !got.ObservationAvailable || got.InferenceReady || got.Enabled == nil || !*got.Enabled || got.Reason != "controller_stopped" || got.Controller.State != assistantidle.StateDisabled || got.Controller.Gate.Allow || len(got.Pods) != 0 {
		t.Fatalf("wrong stopped state: %s", w.Body.String())
	}
}
