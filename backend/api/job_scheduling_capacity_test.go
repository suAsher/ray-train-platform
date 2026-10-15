package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func jobSchedulingCapacityRouter(h *Handler, principal *auth.Principal) *gin.Engine {
	router := gin.New()
	if principal != nil {
		router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", *principal); c.Next() })
	}
	h.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func TestJobSchedulingCapacityAuthorizationPrecedesObservation(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal *auth.Principal
		want      int
	}{
		{"anonymous", nil, http.StatusUnauthorized},
		{"write-only-pat", &auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}, http.StatusForbidden},
		{"other-team", &auth.Principal{Subject: "member", TenantID: "team-b", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 24, GPUAvailable: 24}}
			h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{diagnosisTestJob()}}, Options{Quota: quota})
			r := httptest.NewRecorder()
			jobSchedulingCapacityRouter(h, test.principal).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/scheduling-capacity", nil))
			if r.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", r.Code, test.want, r.Body)
			}
			if len(quota.tenantIDs) != 0 {
				t.Fatal("unauthorized request observed capacity")
			}
		})
	}
}

func TestJobSchedulingCapacityEarlyStatesDoNotQueryLogs(t *testing.T) {
	principal := &auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}
	for _, state := range []domain.State{domain.StateSubmitted, domain.StateValidating, domain.StateQueued, domain.StateAdmitted, domain.StateProvisioning} {
		t.Run(string(state), func(t *testing.T) {
			job := diagnosisTestJob()
			job.ObservedState = state
			logs := &diagnosisTestProvider{}
			h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Logs: logs})
			r := httptest.NewRecorder()
			started := time.Now().UTC()
			jobSchedulingCapacityRouter(h, principal).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/scheduling-capacity", nil))
			assertCapacityServerTime(t, started, r)
			var body struct {
				Data domain.SchedulingCapacity `json:"data"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if r.Code != http.StatusOK || r.Header().Get("Cache-Control") != "no-store" || body.Data.ObservedAt.IsZero() || body.Data.UnavailableReason == "" || body.Data.UnavailableReason == "not_applicable" {
				t.Fatalf("status=%d body=%s", r.Code, r.Body)
			}
			if logs.calls != 0 || logs.contextCalls != 0 {
				t.Fatal("capacity polling queried retained logs")
			}
		})
	}
}

func TestJobSchedulingCapacityRunningOrTerminalIsNotApplicable(t *testing.T) {
	principal := &auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}
	for _, state := range []domain.State{domain.StateRunning, domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut} {
		t.Run(string(state), func(t *testing.T) {
			job := diagnosisTestJob()
			job.ObservedState = state
			quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 24, GPUAvailable: 24}}
			h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Quota: quota})
			r := httptest.NewRecorder()
			started := time.Now().UTC()
			jobSchedulingCapacityRouter(h, principal).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/scheduling-capacity", nil))
			assertCapacityServerTime(t, started, r)
			var body struct {
				Data domain.SchedulingCapacity `json:"data"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if r.Code != http.StatusOK || body.Data.Available || body.Data.UnavailableReason != "not_applicable" {
				t.Fatalf("status=%d body=%s", r.Code, r.Body)
			}
			if len(quota.tenantIDs) != 0 {
				t.Fatal("non-scheduling state queried capacity")
			}
		})
	}
}
