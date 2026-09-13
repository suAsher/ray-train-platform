package api

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	mr "ray-train-platform-backend/modelrelease"
	ms "ray-train-platform-backend/modelserving"
	"strings"
	"testing"
	"time"
)

type servingWorkflowFake struct {
	ms.Store
	contract                           ms.Contract
	deployment                         ms.Deployment
	reserveCalls, markCalls, stopCalls int
	failCalls                          int
	observed                           []string
}

func (s *servingWorkflowFake) GetContract(context.Context, string) (ms.Contract, error) {
	return s.contract, nil
}
func (s *servingWorkflowFake) FindDeploymentRequest(context.Context, string, string, string) (ms.Deployment, error) {
	if s.deployment.ID == "" {
		return ms.Deployment{}, ms.ErrNotFound
	}
	return s.deployment, nil
}
func (s *servingWorkflowFake) ReserveDeployment(_ context.Context, d ms.Deployment) (ms.Deployment, bool, error) {
	s.reserveCalls++
	s.deployment = d
	return d, true, nil
}
func (s *servingWorkflowFake) MarkDeploymentSubmitted(context.Context, string, string) error {
	s.markCalls++
	s.deployment.State = ms.Submitted
	return nil
}
func (s *servingWorkflowFake) FailDeploymentSubmission(context.Context, string) error {
	s.failCalls++
	s.deployment.State = ms.Failed
	return nil
}
func (s *servingWorkflowFake) GetDeployment(context.Context, string) (ms.Deployment, error) {
	return s.deployment, nil
}
func (s *servingWorkflowFake) RequestStop(_ context.Context, _ string, _ string, revision int64) (ms.Deployment, error) {
	s.stopCalls++
	s.deployment.State = ms.Stopping
	s.deployment.Revision = revision + 1
	return s.deployment, nil
}
func (s *servingWorkflowFake) UpdateObserved(_ context.Context, _ string, state string, _ int64) (ms.Deployment, error) {
	s.observed = append(s.observed, state)
	s.deployment.State = state
	return s.deployment, nil
}
func servingWorkflowHandler() (*Handler, *servingWorkflowFake, *evaluationSubmissionFake, *releaseAPIFake) {
	h, _, submit := evaluationTestHandler()
	models := h.models.(*modelStoreFake)
	models.model.OwnerID = streamingPrincipal().Subject
	release := &releaseAPIFake{release: mr.Release{ID: "release-1", ModelID: models.model.ID, VersionID: models.version.ID, ModelSHA256: models.version.SHA256, State: mr.Approved}}
	s := &servingWorkflowFake{contract: ms.Contract{ID: "contract-1", Active: true, ImageReference: streamingTestImage, ImageDigest: "sha256:" + strings.Repeat("a", 64), Code: &me.CodeSnapshot{ID: strings.Repeat("b", 32), SHA256: strings.Repeat("d", 64), SizeBytes: 123, Format: "zip"}, EntryPoint: []string{"python", "serve.py"}}}
	h.modelReleases = release
	h.modelServing = s
	return h, s, submit, release
}
func servingWorkflowRouter(h *Handler, p auth.Principal) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	h.RegisterModelServingReadRoutes(r.Group("/api/v1"))
	h.RegisterModelServingManagementRoutes(r.Group("/api/v1"))
	return r
}
func servingWorkflowBody() string {
	return `{"name":"service","releaseId":"release-1","contractId":"contract-1","ttlSeconds":3600,"resources":{"workerReplicas":1,"gpusPerWorker":1,"cpuPerWorker":4,"memoryPerWorker":"16Gi"}}`
}
func TestModelServingPreflightReserveSubmitAndRetry(t *testing.T) {
	h, s, submit, _ := servingWorkflowHandler()
	r := servingWorkflowRouter(h, streamingPrincipal())
	body := servingWorkflowBody()
	preflight := evaluationTestRequest(r, "POST", "/api/v1/model-services/preflight", body)
	if preflight.Code != 200 || s.reserveCalls != 0 || submit.calls != 0 || submit.preflights != 1 {
		t.Fatalf("preflight %d %s", preflight.Code, preflight.Body.String())
	}
	created := evaluationTestRequest(r, "POST", "/api/v1/model-services", body)
	if created.Code != 202 || s.reserveCalls != 1 || submit.calls != 1 || s.markCalls != 1 {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	in := submit.last
	d := s.deployment
	if in.Origin != domain.SubmissionOriginServing || in.ReservedJobID != d.JobID || in.ExternalSubmissionID != d.ID || in.IdempotencyKey != "model-serving:"+d.ID || in.ExpectedImageDigest != s.contract.ImageDigest {
		t.Fatalf("unbound serving submission %+v", in)
	}
	if in.Spec.Source.Type != "serving-archive" || in.Spec.Source.ArtifactID != s.contract.Code.ID || in.Spec.Source.ArtifactSHA256 != s.contract.Code.SHA256 || in.Spec.TimeoutSeconds != 3600 || in.Spec.Resources.WorkerReplicas != 1 || in.Spec.Resources.GPUsPerWorker != 1 || in.Spec.Output.RelativePath != "services/"+d.ID {
		t.Fatalf("unfrozen serving job %+v", in.Spec)
	}
	if d.ExpiresAt.Sub(d.CreatedAt) != time.Hour || d.ModelSHA256 == "" || d.ModelSizeBytes != 4 {
		t.Fatalf("bad reservation %+v", d)
	}
	retry := evaluationTestRequest(r, "POST", "/api/v1/model-services", body)
	if retry.Code != 200 || s.reserveCalls != 1 || submit.calls != 1 || s.markCalls != 1 {
		t.Fatalf("retry repeated work %d %s", retry.Code, retry.Body.String())
	}
	conflict := evaluationTestRequest(r, "POST", "/api/v1/model-services", strings.Replace(body, `"name":"service"`, `"name":"different"`, 1))
	if conflict.Code != 409 || submit.calls != 1 {
		t.Fatalf("changed idempotent request %d", conflict.Code)
	}
}
func TestModelServingSubmitFailureReleasesReservation(t *testing.T) {
	h, s, submit, _ := servingWorkflowHandler()
	submit.submitErr = errors.New("scheduler rejected submission")
	w := evaluationTestRequest(servingWorkflowRouter(h, streamingPrincipal()), "POST", "/api/v1/model-services", servingWorkflowBody())
	if w.Code != 500 || s.reserveCalls != 1 || submit.calls != 1 || s.failCalls != 1 || s.deployment.State != ms.Failed {
		t.Fatalf("submit failure did not release reservation: code=%d reserve=%d submit=%d fail=%d state=%s body=%s", w.Code, s.reserveCalls, submit.calls, s.failCalls, s.deployment.State, w.Body.String())
	}
}
func TestModelServingRequiresApprovalOwnerOneGPUAndBoundedTTL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Handler, *servingWorkflowFake, *releaseAPIFake)
		body   string
		want   int
	}{
		{name: "pending approval", change: func(_ *Handler, _ *servingWorkflowFake, r *releaseAPIFake) { r.release.State = mr.Pending }, want: 409},
		{name: "rejected approval", change: func(_ *Handler, _ *servingWorkflowFake, r *releaseAPIFake) { r.release.State = mr.Rejected }, want: 409},
		{name: "foreign owner", change: func(h *Handler, _ *servingWorkflowFake, _ *releaseAPIFake) {
			h.models.(*modelStoreFake).model.OwnerID = "another-user"
		}, want: 403},
		{name: "archive", change: func(h *Handler, _ *servingWorkflowFake, _ *releaseAPIFake) {
			h.models.(*modelStoreFake).model.Archived = true
		}, want: 409},
		{name: "disabled contract", change: func(_ *Handler, s *servingWorkflowFake, _ *releaseAPIFake) { s.contract.Active = false }, want: 409},
		{name: "sha drift", change: func(_ *Handler, _ *servingWorkflowFake, r *releaseAPIFake) {
			r.release.ModelSHA256 = strings.Repeat("e", 64)
		}, want: 409},
		{name: "two GPU", body: strings.Replace(servingWorkflowBody(), `"gpusPerWorker":1`, `"gpusPerWorker":2`, 1), want: 400},
		{name: "two workers", body: strings.Replace(servingWorkflowBody(), `"workerReplicas":1`, `"workerReplicas":2`, 1), want: 400},
		{name: "ttl too short", body: strings.Replace(servingWorkflowBody(), `"ttlSeconds":3600`, `"ttlSeconds":3599`, 1), want: 400},
		{name: "ttl too long", body: strings.Replace(servingWorkflowBody(), `"ttlSeconds":3600`, `"ttlSeconds":604801`, 1), want: 400},
		{name: "unknown field", body: strings.Replace(servingWorkflowBody(), `"name":"service"`, `"name":"service","ownerId":"admin"`, 1), want: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, submit, release := servingWorkflowHandler()
			if tc.change != nil {
				tc.change(h, s, release)
			}
			body := tc.body
			if body == "" {
				body = servingWorkflowBody()
			}
			w := evaluationTestRequest(servingWorkflowRouter(h, streamingPrincipal()), "POST", "/api/v1/model-services", body)
			if w.Code != tc.want || s.reserveCalls != 0 || submit.calls != 0 {
				t.Fatalf("%d %s calls %d/%d", w.Code, w.Body.String(), s.reserveCalls, submit.calls)
			}
		})
	}
}
func TestModelServingStopIsOwnerOnlyAndDoesNotPretendReclaimed(t *testing.T) {
	h, s, _, _ := servingWorkflowHandler()
	s.deployment = ms.Deployment{ID: "service-1", OwnerID: "owner", State: ms.Ready, Revision: 1}
	p := streamingPrincipal()
	r := servingWorkflowRouter(h, p)
	w := evaluationTestRequest(r, "POST", "/api/v1/model-services/service-1/stop", `{"revision":1}`)
	if w.Code != 403 || s.stopCalls != 0 {
		t.Fatalf("foreign stop %d", w.Code)
	}
	p.Subject = "owner"
	w = evaluationTestRequest(servingWorkflowRouter(h, p), "POST", "/api/v1/model-services/service-1/stop", `{"revision":1}`)
	if w.Code != 202 || s.deployment.State != ms.Stopping {
		t.Fatalf("stop did not retain resource reservation %d", w.Code)
	}
}
func TestModelServingControllerWaitsForTerminalJobBeforeReleasing(t *testing.T) {
	for _, kind := range []string{"expired", "stopping", "terminal", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			h, s, _, _ := servingWorkflowHandler()
			d := ms.Deployment{ID: "service-1", JobID: "job-service", OwnerID: "owner", TenantID: "tenant", State: ms.Ready, Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
			job := domain.TrainingJob{ID: d.JobID, UserID: d.OwnerID, TenantID: d.TenantID, SubmissionOrigin: domain.SubmissionOriginServing, ExternalSubmissionID: d.ID, Spec: d.JobSpec, ObservedState: domain.StateRunning}
			if kind == "expired" || kind == "unknown" {
				d.ExpiresAt = time.Now().Add(-time.Minute)
			}
			if kind == "stopping" {
				d.State = ms.Stopping
			}
			if kind == "terminal" {
				d.State = ms.Stopping
				job.ObservedState = domain.StateCanceled
			}
			if kind == "unknown" {
				d.State = ms.Creating
			}
			s.deployment = d
			repo := &fakeJobRepository{}
			if kind != "unknown" {
				repo.jobs = []domain.TrainingJob{job}
			}
			h.repository = repo
			h.reconcileModelService(context.Background(), d)
			if kind == "terminal" {
				if s.deployment.State != ms.Stopped {
					t.Fatalf("terminal not reclaimed %+v", s)
				}
			} else {
				if s.deployment.State != ms.Stopping {
					t.Fatalf("nonterminal released %+v", s)
				}
				for _, state := range s.observed {
					if ms.Terminal(state) {
						t.Fatalf("nonterminal job released as %s", state)
					}
				}
				if kind != "unknown" && repo.canceled == "" {
					t.Fatal("cancellation not requested")
				}
			}
		})
	}
}
