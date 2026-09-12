package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	"ray-train-platform-backend/modellifecycle"
)

type evaluationStoreFake struct {
	ModelEvaluationStore
	evaluator                                                          me.Evaluator
	evaluation                                                         me.Evaluation
	reserveCalls, submittedCalls, reportCalls, tokenCalls, createCalls int
	err                                                                error
	cancelReservation                                                  bool
}

func (s *evaluationStoreFake) GetEvaluator(_ context.Context, id string) (me.Evaluator, error) {
	if id != s.evaluator.ID { return me.Evaluator{}, me.ErrNotFound }
	return s.evaluator, s.err
}
func (s *evaluationStoreFake) ListEvaluators(context.Context, bool) ([]me.Evaluator, error) {
	return []me.Evaluator{s.evaluator}, s.err
}
func (s *evaluationStoreFake) CreateEvaluator(_ context.Context, e me.Evaluator) (me.Evaluator, error) {
	s.createCalls++
	s.evaluator = e
	return e, s.err
}
func (s *evaluationStoreFake) FindEvaluationRequest(context.Context, string, string, string) (me.Evaluation, error) {
	if s.evaluation.ID != "" {
		return s.evaluation, nil
	}
	return me.Evaluation{}, me.ErrNotFound
}
func (s *evaluationStoreFake) ReserveEvaluation(_ context.Context, e me.Evaluation) (me.Evaluation, bool, error) {
	s.reserveCalls++
	s.evaluation = e
	return e, true, s.err
}
func (s *evaluationStoreFake) MarkEvaluationSubmitted(context.Context, string, string) error {
	s.submittedCalls++
	s.evaluation.State = me.Submitted
	return s.err
}
func (s *evaluationStoreFake) FailEvaluationSubmission(context.Context, string, string) error {
	return s.err
}
func (s *evaluationStoreFake) CancelEvaluationReservation(context.Context, string) (bool, error) {
	return s.cancelReservation, s.err
}
func (s *evaluationStoreFake) GetEvaluation(context.Context, string, string, bool) (me.Evaluation, error) {
	return s.evaluation, s.err
}
func (s *evaluationStoreFake) ListEvaluations(context.Context, me.Filter) (me.EvaluationPage, error) {
	return me.EvaluationPage{Items: []me.Evaluation{s.evaluation}}, s.err
}
func (s *evaluationStoreFake) AuthorizeEvaluationJobToken(context.Context, string, []byte, time.Time) (me.Evaluation, error) {
	s.tokenCalls++
	return s.evaluation, s.err
}
func (s *evaluationStoreFake) StoreEvaluationReport(context.Context, string, []byte, []byte, time.Time) (me.Evaluation, error) {
	s.reportCalls++
	return s.evaluation, s.err
}

type evaluationSubmissionFake struct {
	last              SubmissionInput
	calls, preflights int
	err               error
}

func (s *evaluationSubmissionFake) Preflight(_ context.Context, in SubmissionInput) (SubmissionPreflightResult, error) {
	s.last = in
	s.preflights++
	return SubmissionPreflightResult{Image: streamingTestImage, TrainingEngine: domain.TrainingEngineRayTrain, RayVersion: domain.RayVersionCanary, RequestedGPUs: in.Spec.Resources.GPUsPerWorker}, s.err
}
func (s *evaluationSubmissionFake) Submit(_ context.Context, in SubmissionInput) (*domain.TrainingJob, error) {
	s.last = in
	s.calls++
	return &domain.TrainingJob{ID: in.ReservedJobID, TenantID: in.Principal.TenantID, UserID: in.Principal.Subject, SubmissionOrigin: in.Origin, ExternalSubmissionID: in.ExternalSubmissionID, Spec: in.Spec}, s.err
}
func evaluationTestHandler() (*Handler, *evaluationStoreFake, *evaluationSubmissionFake) {
	ds, dv := streamingDatasetFixtures()
	dv.ValSamples = 12
	evaluator := me.Evaluator{ID: "evaluator-1", Name: "fixed evaluator", OwnerID: "admin", TenantID: "tenant-a", Active: true, Revision: 1, ImageReference: streamingTestImage, ImageDigest: "sha256:" + strings.Repeat("a", 64), GitURL: "https://git.example/evaluator.git", GitCommit: strings.Repeat("b", 40), EntryPoint: []string{"python", "evaluate.py"}, SchemaVersion: dv.SchemaVersion, Protocol: me.Protocol}
	store := &evaluationStoreFake{evaluator: evaluator}
	submit := &evaluationSubmissionFake{}
	models := &modelStoreFake{model: modellifecycle.Model{ID: "model-1"}, version: modellifecycle.Version{ID: "version-1", ModelID: "model-1", State: modellifecycle.Ready, SHA256: strings.Repeat("c", 64), FileName: "weights.pt", SizeBytes: 4}}
	h := NewHandler(&fakeJobRepository{}, Options{Models: models, ModelEvaluations: store, ModelEvaluationSubmission: submit, Datasets: &fakeDatasetCatalog{datasets: []domain.Dataset{ds}, versions: []domain.DatasetVersion{dv}}})
	return h, store, submit
}
func evaluationTestRouter(h *Handler, p auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	h.RegisterModelEvaluationReadRoutes(r.Group("/api/v1"))
	h.RegisterModelEvaluationManagementRoutes(r.Group("/api/v1"))
	h.RegisterModelEvaluationInternalRoutes(r.Group("/api/v1/internal"))
	return r
}
func evaluationTestBody() string {
	return `{"modelId":"model-1","versionId":"version-1","datasetId":"dataset-labeled-full","datasetVersionId":"version-20260830","evaluatorId":"evaluator-1","split":"val","sites":[],"config":{"threshold":0.5},"resources":{"workerReplicas":1,"gpusPerWorker":1,"cpuPerWorker":4,"memoryPerWorker":"16Gi"}}`
}
func evaluationTestRequest(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "evaluation-request-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func TestModelEvaluationPreflightFreezesActualSources(t *testing.T) {
	h, _, submit := evaluationTestHandler()
	w := evaluationTestRequest(evaluationTestRouter(h, streamingPrincipal()), "POST", "/api/v1/model-evaluations/preflight", evaluationTestBody())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var data struct {
		Evaluation me.Evaluation  `json:"evaluation"`
		JobSpec    domain.JobSpec `json:"jobSpec"`
	}
	decodeTrackingData(t, w, &data)
	if data.Evaluation.Dataset.SampleCount != 12 || data.Evaluation.Dataset.Split != "val" || data.Evaluation.ModelSHA256 != strings.Repeat("c", 64) || data.Evaluation.Evaluator.GitCommit != strings.Repeat("b", 40) || submit.preflights != 1 {
		t.Fatalf("sources not frozen: %+v", data)
	}
	if submit.last.Origin != domain.SubmissionOriginEvaluation || submit.last.Spec.DatasetRef.Version == "latest" || submit.last.ExpectedDatasetManifestSHA256 == "" || submit.calls != 0 {
		t.Fatalf("unsafe preflight: %+v", submit.last)
	}
}
func TestModelEvaluationRejectsUnusableSourcesBeforeSubmission(t *testing.T) {
	for _, change := range []string{"archived", "notready", "disabled", "schema", "test-empty", "sites", "latest"} {
		t.Run(change, func(t *testing.T) {
			h, s, submit := evaluationTestHandler()
			body := evaluationTestBody()
			switch change {
			case "archived":
				h.models.(*modelStoreFake).model.Archived = true
			case "notready":
				h.models.(*modelStoreFake).version.State = modellifecycle.Copying
			case "disabled":
				s.evaluator.Active = false
			case "schema":
				s.evaluator.SchemaVersion = "wrong"
			case "test-empty":
				body = strings.Replace(body, `"split":"val"`, `"split":"test"`, 1)
				h.datasets.(*fakeDatasetCatalog).versions[0].TestSamples = 0
			case "sites":
				body = strings.Replace(body, `"sites":[]`, `"sites":["unknown"]`, 1)
			case "latest":
				body = strings.Replace(body, `"version-20260830"`, `"latest"`, 1)
			}
			w := evaluationTestRequest(evaluationTestRouter(h, streamingPrincipal()), "POST", "/api/v1/model-evaluations", body)
			if w.Code < 400 || submit.calls != 0 || s.reserveCalls != 0 {
				t.Fatalf("invalid source accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestModelEvaluationCreatePinsJobAndReplays(t *testing.T) {
	h, s, submit := evaluationTestHandler()
	r := evaluationTestRouter(h, streamingPrincipal())
	w := evaluationTestRequest(r, "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 202 || submit.calls != 1 || s.reserveCalls != 1 || s.submittedCalls != 1 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	id := s.evaluation.ID
	jobID := s.evaluation.JobID
	if len(jobID) != 28 || submit.last.ReservedJobID != jobID || submit.last.ExternalSubmissionID != id || submit.last.Spec.Output.Space != domain.DataSpaceMyRuns || submit.last.Spec.Output.RelativePath != "evaluations/"+id {
		t.Fatalf("unsafe reservation/output %+v", submit.last)
	}
	w = evaluationTestRequest(r, "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 200 || submit.calls != 1 || s.evaluation.ID != id {
		t.Fatalf("retry duplicated execution: %d calls=%d", w.Code, submit.calls)
	}
}
func TestModelEvaluationJSONRejectsIdentityAndDuplicates(t *testing.T) {
	for _, body := range []string{strings.Replace(evaluationTestBody(), `"modelId":"model-1"`, `"modelId":"model-1","modelId":"other"`, 1), strings.Replace(evaluationTestBody(), `"modelId":"model-1"`, `"ModelId":"model-1"`, 1), strings.Replace(evaluationTestBody(), `"modelId":"model-1"`, `"ownerId":"other","modelId":"model-1"`, 1), strings.Replace(evaluationTestBody(), `"threshold":0.5`, `"threshold":0.5,"threshold":0.9`, 1)} {
		h, _, submit := evaluationTestHandler()
		w := evaluationTestRequest(evaluationTestRouter(h, streamingPrincipal()), "POST", "/api/v1/model-evaluations", body)
		if w.Code != 400 || submit.calls != 0 {
			t.Fatalf("ambiguous request accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestModelEvaluationMachineCannotManageAndMemberCannotCreateEvaluator(t *testing.T) {
	h, s, _ := evaluationTestHandler()
	p := streamingPrincipal()
	p.AuthType = auth.AuthTypePAT
	p.Scopes = []string{domain.PATScopeJobsRead, domain.PATScopeJobsWrite}
	w := evaluationTestRequest(evaluationTestRouter(h, p), "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 403 {
		t.Fatalf("PAT allowed create: %d", w.Code)
	}
	w = evaluationTestRequest(evaluationTestRouter(h, streamingPrincipal()), "POST", "/api/v1/model-evaluators", `{}`)
	if w.Code != 403 || s.createCalls != 0 {
		t.Fatalf("member created evaluator: %d", w.Code)
	}
}
func TestModelEvaluationReadAndReportKeepDatasetACL(t *testing.T) {
	h, s, _ := evaluationTestHandler()
	s.evaluation = me.Evaluation{ID: "evaluation-1", TenantID: "foreign", OwnerID: "other", State: me.Succeeded, ReportState: me.ReportPending, Dataset: me.DatasetSnapshot{Visibility: me.Team, TenantID: "foreign"}}
	r := evaluationTestRouter(h, streamingPrincipal())
	for _, path := range []string{"/api/v1/model-evaluations/evaluation-1", "/api/v1/model-evaluations/evaluation-1/report"} {
		w := evaluationTestRequest(r, "GET", path, "")
		if w.Code != 404 {
			t.Fatalf("TEAM report leaked: %d %s", w.Code, w.Body.String())
		}
	}
	s.evaluation.Dataset.Visibility = me.Public
	s.evaluation.Dataset.TenantID = ""
	w := evaluationTestRequest(r, "GET", "/api/v1/model-evaluations/evaluation-1/report", "")
	if w.Code != 409 {
		t.Fatalf("success without valid report exposed: %d", w.Code)
	}
}
func TestModelEvaluationInternalTokenRejectsBeforeModelRead(t *testing.T) {
	h, s, _ := evaluationTestHandler()
	h.modelSnapshots = &modelSnapshotsFake{}
	r := evaluationTestRouter(h, streamingPrincipal())
	w := evaluationTestRequest(r, "GET", "/api/v1/internal/jobs/job-0123456789abcdef01234567/model-evaluation/model", "")
	if w.Code != 401 || s.tokenCalls != 0 {
		t.Fatalf("missing token accepted: %d", w.Code)
	}
	s.err = me.ErrNotFound
	req := httptest.NewRequest("POST", "/api/v1/internal/jobs/job-0123456789abcdef01234567/model-evaluation/report", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 || s.reportCalls != 0 {
		t.Fatalf("report reached store without valid job credential: %d", w.Code)
	}
}

func TestModelEvaluatorRegistrationResolvesCatalogueDigest(t *testing.T) {
	h, s, _ := evaluationTestHandler()
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleSuperAdmin}
	h.modelEvaluationSubmission = streamingSubmissionService(&submissionServiceRepository{}, &fakeDatasetCatalog{}, true, true, domain.RayVersionCanary, nil)
	body := `{"name":"fixed plan","description":"validation","imageReference":"` + streamingTestImage + `","gitUrl":"https://git.example/evaluator.git","gitCommit":"` + strings.Repeat("b", 40) + `","entryPoint":["python","evaluate.py"],"schemaVersion":"s1h-v1"}`
	w := evaluationTestRequest(evaluationTestRouter(h, p), "POST", "/api/v1/model-evaluators", body)
	if w.Code != 201 || s.createCalls != 1 || s.evaluator.ImageDigest != "sha256:"+strings.Repeat("a", 64) || s.evaluator.ImageReference != streamingTestImage {
		t.Fatalf("catalogue digest not frozen: %d %s", w.Code, w.Body.String())
	}
}
func TestModelEvaluationCreatingReplayUsesPersistedReservation(t *testing.T) {
	h, s, submit := evaluationTestHandler()
	r := evaluationTestRouter(h, streamingPrincipal())
	w := evaluationTestRequest(r, "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 202 {
		t.Fatalf("setup %d %s", w.Code, w.Body.String())
	}
	original := s.evaluation
	s.evaluation.State = me.Creating
	s.evaluator.Active = false
	h.models.(*modelStoreFake).model.Archived = true
	w = evaluationTestRequest(r, "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 202 || s.reserveCalls != 1 || submit.last.ReservedJobID != original.JobID || submit.last.Spec.Output.RelativePath != original.JobSpec.Output.RelativePath {
		t.Fatalf("replay discarded frozen reservation: %d %s", w.Code, w.Body.String())
	}
}
func TestModelEvaluationReportDownloadAndOwnerCancellation(t *testing.T) {
	h, s, submit := evaluationTestHandler()
	p := streamingPrincipal()
	r := evaluationTestRouter(h, p)
	w := evaluationTestRequest(r, "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 202 {
		t.Fatalf("setup %d %s", w.Code, w.Body.String())
	}
	e := s.evaluation
	job := domain.TrainingJob{ID: e.JobID, TenantID: e.TenantID, UserID: e.OwnerID, SubmissionOrigin: domain.SubmissionOriginEvaluation, ExternalSubmissionID: e.ID, Spec: submit.last.Spec}
	h.repository.(*fakeJobRepository).jobs = []domain.TrainingJob{job}
	w = evaluationTestRequest(r, "POST", "/api/v1/model-evaluations/"+e.ID+"/cancel", "")
	if w.Code != 202 || h.repository.(*fakeJobRepository).canceled == "" {
		t.Fatalf("owner cannot cancel %d %s", w.Code, w.Body.String())
	}
	report := me.Report{Protocol: me.Protocol, EvaluationID: e.ID, ModelSHA256: e.ModelSHA256, DatasetManifestSHA256: e.Dataset.ManifestSHA256, EvaluatorID: e.Evaluator.ID, ConfigSHA256: e.ConfigSHA256, Metrics: []me.Metric{{Name: "accuracy", Value: 0.9, Unit: "ratio", Direction: me.Higher}}}
	raw, _ := json.Marshal(report)
	_, hash, err := me.ValidateReport(raw, e)
	if err != nil {
		t.Fatal(err)
	}
	s.evaluation.Report = &report
	s.evaluation.ReportState = me.ReportValid
	s.evaluation.ReportSHA256 = hash
	w = evaluationTestRequest(r, "GET", "/api/v1/model-evaluations/"+e.ID+"/report", "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Content-SHA256") != hash || w.Body.String() != string(raw) {
		t.Fatalf("report is not faithful attachment %d %s", w.Code, w.Body.String())
	}
}
func TestModelEvaluationInternalModelAndReportUseExactJobToken(t *testing.T) {
	h, s, _ := evaluationTestHandler()
	r := evaluationTestRouter(h, streamingPrincipal())
	w := evaluationTestRequest(r, "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 202 {
		t.Fatalf("setup %d %s", w.Code, w.Body.String())
	}
	h.modelSnapshots = &modelSnapshotsFake{}
	req := httptest.NewRequest("GET", "/api/v1/internal/jobs/"+s.evaluation.JobID+"/model-evaluation/model", nil)
	req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || s.tokenCalls != 1 || w.Header().Get("X-Content-SHA256") != s.evaluation.ModelSHA256 {
		t.Fatalf("model token flow %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "/api/v1/internal/jobs/"+s.evaluation.JobID+"/model-evaluation/report", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || s.reportCalls != 1 || s.tokenCalls != 2 {
		t.Fatalf("report token flow %d %s", w.Code, w.Body.String())
	}
}

func TestModelEvaluationCreatingReservationCanBeCancelled(t *testing.T) {
	h, s, _ := evaluationTestHandler()
	s.evaluation = me.Evaluation{ID: "evaluation-1", OwnerID: streamingPrincipal().Subject, TenantID: streamingPrincipal().TenantID, JobID: "job-0123456789abcdef01234567", State: me.Creating, Dataset: me.DatasetSnapshot{Visibility: me.Public}}
	s.cancelReservation = true
	w := evaluationTestRequest(evaluationTestRouter(h, streamingPrincipal()), "POST", "/api/v1/model-evaluations/evaluation-1/cancel", "")
	if w.Code != 202 || h.repository.(*fakeJobRepository).canceled != "" || !strings.Contains(w.Body.String(), `"state":"CANCELLED"`) {
		t.Fatalf("creating cancellation did not release reservation safely: %d %s", w.Code, w.Body.String())
	}
}
