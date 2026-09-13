package api

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServingHealthRequiresExactDeploymentAndDigestAndRejectsRedirect(t *testing.T) {
	d := ms.Deployment{ID: "deployment", ModelSHA256: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"ready", `{"deploymentId":"deployment","modelSha256":"` + d.ModelSHA256 + `"}`, 200, true},
		{"wrong deployment", `{"deploymentId":"another","modelSha256":"` + d.ModelSHA256 + `"}`, 200, false},
		{"wrong digest", `{"deploymentId":"deployment","modelSha256":"wrong"}`, 200, false},
		{"not JSON", `<html>ready</html>`, 200, false},
		{"too large", strings.Repeat("x", 4097), 200, false},
		{"unavailable", `{}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/healthz" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := checkServingHealth(context.Background(), server.URL, d)
			if (err == nil) != tc.want {
				t.Fatalf("health err %v", err)
			}
		})
	}
	var reached atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"deploymentId": d.ID, "modelSha256": d.ModelSHA256})
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	if err := checkServingHealth(context.Background(), redirect.URL, d); err == nil || reached.Load() != 0 {
		t.Fatalf("health redirect followed: %v calls=%d", err, reached.Load())
	}
}

type servingKubernetesFake struct {
	target  string
	ensured int
	deleted int
}

func (k *servingKubernetesFake) EnsureModelServingService(context.Context, *domain.TrainingJob) (string, error) {
	k.ensured++
	return k.target, nil
}
func (k *servingKubernetesFake) DeleteModelServingService(context.Context, string, string, string) error {
	k.deleted++
	return nil
}
func servingProxyHandler(target string) (*Handler, *servingWorkflowFake, *servingKubernetesFake) {
	h, s, _, _ := servingWorkflowHandler()
	d := ms.Deployment{ID: "service-1", JobID: "job-service", ModelSHA256: strings.Repeat("a", 64), OwnerID: "owner", TenantID: "tenant", State: ms.Ready, ExpiresAt: time.Now().Add(time.Hour)}
	s.deployment = d
	h.repository = &fakeJobRepository{jobs: []domain.TrainingJob{{ID: d.JobID, UserID: d.OwnerID, TenantID: d.TenantID, SubmissionOrigin: domain.SubmissionOriginServing, ExternalSubmissionID: d.ID, Spec: d.JobSpec, ObservedState: domain.StateRunning}}}
	k := &servingKubernetesFake{target: target}
	h.modelServingKubernetes = k
	h.servingRequests = make(chan struct{}, 2)
	return h, s, k
}
func servingInvocationRouter(h *Handler) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", streamingPrincipal()); c.Next() })
	r.POST("/api/v1/model-services/:serviceId/invocations", h.invokeModelService)
	return r
}
func TestServingInvocationForwardsOnlyJSONWithoutCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "Cookie", "X-Forwarded-For", "X-Forwarded-Host", "X-Auth-Request-Access-Token", "X-Api-Key"} {
			if r.Header.Get(name) != "" {
				t.Errorf("leaked %s", name)
			}
		}
		if r.URL.Path == "/healthz" {
			_ = json.NewEncoder(w).Encode(map[string]string{"deploymentId": "service-1", "modelSha256": strings.Repeat("a", 64)})
			return
		}
		if r.URL.Path != "/invocations" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("bad invocation %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != `{"value":7}` {
			t.Errorf("input changed %s", raw)
		}
		calls.Add(1)
		w.Header().Set("Set-Cookie", "model-secret=bad")
		w.Header().Set("Location", "http://not-forwarded.invalid")
		_, _ = io.WriteString(w, `{"result":14}`)
	}))
	defer server.Close()
	h, _, _ := servingProxyHandler(server.URL)
	r := servingInvocationRouter(h)
	req := httptest.NewRequest("POST", "/api/v1/model-services/service-1/invocations", strings.NewReader(`{"value":7}`))
	req.Header.Set("Content-Type", "application/json")
	for _, name := range []string{"Authorization", "Cookie", "X-Forwarded-For", "X-Forwarded-Host", "X-Auth-Request-Access-Token", "X-Api-Key"} {
		req.Header.Set(name, "private-test-value")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != `{"result":14}` || calls.Load() != 1 {
		t.Fatalf("invocation failed %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" {
		t.Fatal("upstream response headers leaked")
	}
}
func TestServingInvocationRejectsRedirectAndInvalidResponse(t *testing.T) {
	for _, kind := range []string{"redirect", "oversized", "invalid", "error"} {
		t.Run(kind, func(t *testing.T) {
			var redirected atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); _, _ = io.WriteString(w, `{}`) }))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					_ = json.NewEncoder(w).Encode(map[string]string{"deploymentId": "service-1", "modelSha256": strings.Repeat("a", 64)})
					return
				}
				switch kind {
				case "redirect":
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
				case "oversized":
					_, _ = io.WriteString(w, strings.Repeat("x", servingMaxPayload+1))
				case "invalid":
					_, _ = io.WriteString(w, "not JSON")
				case "error":
					w.WriteHeader(500)
					_, _ = io.WriteString(w, `{"private":"internal failure"}`)
				}
			}))
			defer server.Close()
			h, _, _ := servingProxyHandler(server.URL)
			req := httptest.NewRequest("POST", "/api/v1/model-services/service-1/invocations", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			servingInvocationRouter(h).ServeHTTP(w, req)
			if w.Code != 502 || redirected.Load() != 0 || strings.Contains(w.Body.String(), "internal failure") {
				t.Fatalf("unsafe response %d %s redirects=%d", w.Code, w.Body.String(), redirected.Load())
			}
		})
	}
}
func TestServingTargetRejectsStoppedExpiredForeignAndCanceledJobs(t *testing.T) {
	for _, kind := range []string{"stopped", "expired", "foreign", "canceled", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			h, s, k := servingProxyHandler("http://unused.invalid")
			d := s.deployment
			repo := h.repository.(*fakeJobRepository)
			switch kind {
			case "stopped":
				d.State = ms.Stopped
			case "expired":
				d.ExpiresAt = time.Now().Add(-time.Minute)
			case "foreign":
				repo.jobs[0].UserID = "other"
			case "canceled":
				repo.jobs[0].DesiredState = domain.DesiredCanceled
			case "terminal":
				repo.jobs[0].ObservedState = domain.StateFailed
			}
			if _, err := h.servingTarget(context.Background(), d); err == nil || k.ensured != 0 {
				t.Fatalf("unsafe target %v ensure=%d", err, k.ensured)
			}
		})
	}
}

func TestServingInvocationPATRequiresExplicitScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_ = json.NewEncoder(w).Encode(map[string]string{"deploymentId": "service-1", "modelSha256": strings.Repeat("a", 64)})
			return
		}
		_, _ = io.WriteString(w, `{"result":true}`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name   string
		scopes []string
		want   int
	}{
		{"old jobs token", []string{domain.PATScopeJobsRead, domain.PATScopeJobsWrite}, 403},
		{"full mlflow token", []string{domain.PATScopeMLflowFull}, 403},
		{"explicit inference", []string{domain.PATScopeModelsInvoke}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := servingProxyHandler(server.URL)
			p := streamingPrincipal()
			p.AuthType = auth.AuthTypePAT
			p.Scopes = tc.scopes
			r := servingWorkflowRouter(h, p)
			w := evaluationTestRequest(r, "POST", "/api/v1/model-services/service-1/invocations", `{}`)
			if w.Code != tc.want {
				t.Fatalf("scope boundary %d %s", w.Code, w.Body.String())
			}
			manage := evaluationTestRequest(r, "POST", "/api/v1/model-services", servingWorkflowBody())
			if manage.Code != 403 {
				t.Fatalf("PAT gained management %d", manage.Code)
			}
		})
	}
}
