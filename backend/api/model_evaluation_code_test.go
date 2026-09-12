package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

type evaluationCodeStoreFake struct {
	published, opened int
	code              me.CodeSnapshot
	publishID         string
	source            domain.SourceArtifact
	err               error
	content           string
	size              int64
	reader            *countingReadCloser
}

func (s *evaluationCodeStoreFake) Publish(_ context.Context, id string, artifact domain.SourceArtifact) (me.CodeSnapshot, error) {
	s.published++
	s.publishID = id
	s.source = artifact
	if s.err != nil {
		return me.CodeSnapshot{}, s.err
	}
	s.code = me.CodeSnapshot{ID: id, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Format: "zip"}
	return s.code, nil
}
func (s *evaluationCodeStoreFake) Open(_ context.Context, code me.CodeSnapshot) (io.ReadCloser, int64, error) {
	s.opened++
	s.code = code
	if s.err != nil {
		return nil, 0, s.err
	}
	s.reader = &countingReadCloser{reader: strings.NewReader(s.content)}
	return s.reader, s.size, nil
}

type evaluationSourceArtifactFake struct {
	artifact         domain.SourceArtifact
	tenant, user, id string
	err              error
}

func (s *evaluationSourceArtifactFake) GetSourceArtifact(_ context.Context, tenant, user, id string) (*domain.SourceArtifact, error) {
	s.tenant, s.user, s.id = tenant, user, id
	return &s.artifact, s.err
}
func evaluationArchiveTestSetup() (*Handler, *evaluationStoreFake, *evaluationSubmissionFake, *evaluationCodeStoreFake, *evaluationSourceArtifactFake) {
	h, store, submit := evaluationTestHandler()
	code := &evaluationCodeStoreFake{}
	p := streamingPrincipal()
	source := &evaluationSourceArtifactFake{artifact: domain.SourceArtifact{ID: "artifact-upload", TenantID: p.TenantID, UserID: p.Subject, SHA256: strings.Repeat("d", 64), SizeBytes: 123, State: domain.SourceArtifactReady, ObjectKey: "private-upload-object"}}
	h.evaluationCode = code
	h.evaluationSourceArtifacts = source
	return h, store, submit, code, source
}
func evaluationArchivePlanBody() string {
	return `{"name":"uploaded evaluator","imageReference":"` + streamingTestImage + `","sourceArtifactId":"artifact-upload","entryPoint":["python","evaluate.py"],"schemaVersion":"s1h-v1"}`
}
func TestModelEvaluatorCodeRegistrationFreezesOwnedUploadAndReplays(t *testing.T) {
	h, store, submit, code, source := evaluationArchiveTestSetup()
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleSuperAdmin}
	r := evaluationTestRouter(h, p)
	w := evaluationTestRequest(r, "POST", "/api/v1/model-evaluators", evaluationArchivePlanBody())
	if w.Code != 201 || code.published != 1 || source.tenant != p.TenantID || source.user != p.Subject || source.id != "artifact-upload" || store.evaluator.Code == nil || store.evaluator.GitURL != "" || store.evaluator.GitCommit != "" {
		t.Fatalf("code registration unsafe: %d %s %+v", w.Code, w.Body.String(), store.evaluator)
	}
	if submit.last.Origin != domain.SubmissionOriginPortal || submit.last.Spec.Source.Type != "workspace-archive" || submit.last.Spec.Source.ArtifactID != "artifact-upload" {
		t.Fatalf("archive registration bypassed ordinary preflight: %+v", submit.last)
	}
	id := store.evaluator.ID
	if len(id) != 32 || store.evaluator.Code.ID != code.publishID || store.evaluator.Code.SHA256 != source.artifact.SHA256 {
		t.Fatalf("not frozen %+v", store.evaluator)
	}
	w = evaluationTestRequest(r, "POST", "/api/v1/model-evaluators", evaluationArchivePlanBody())
	if w.Code != 200 || code.published != 1 || store.createCalls != 1 || store.evaluator.ID != id {
		t.Fatalf("registration retry duplicated snapshot/plan: %d %s", w.Code, w.Body.String())
	}
	w = evaluationTestRequest(r, "POST", "/api/v1/model-evaluators", strings.Replace(evaluationArchivePlanBody(), "uploaded evaluator", "changed plan", 1))
	if w.Code != 409 || code.published != 1 {
		t.Fatalf("key reuse changed immutable plan: %d %s", w.Code, w.Body.String())
	}
}
func TestModelEvaluatorCodeRejectsForeignUnreadyOversizeMixedAndPublishFailure(t *testing.T) {
	for _, change := range []string{"owner", "tenant", "pending", "oversize", "mixed-git", "publish", "member"} {
		t.Run(change, func(t *testing.T) {
			h, store, _, code, source := evaluationArchiveTestSetup()
			p := streamingPrincipal()
			p.Roles = []string{domain.RoleSuperAdmin}
			body := evaluationArchivePlanBody()
			switch change {
			case "owner":
				source.artifact.UserID = "other"
			case "tenant":
				source.artifact.TenantID = "other"
			case "pending":
				source.artifact.State = domain.SourceArtifactPending
			case "oversize":
				source.artifact.SizeBytes = me.MaxEvaluationCodeSize + 1
			case "mixed-git":
				body = strings.Replace(body, `"sourceArtifactId":"artifact-upload"`, `"sourceArtifactId":"artifact-upload","gitUrl":"https://git.example/eval.git","gitCommit":"`+strings.Repeat("b", 40)+`"`, 1)
			case "publish":
				code.err = errors.New("private TOS key")
			case "member":
				p.Roles = []string{domain.RoleEngineer}
			}
			w := evaluationTestRequest(evaluationTestRouter(h, p), "POST", "/api/v1/model-evaluators", body)
			if w.Code < 400 || store.createCalls != 0 || (change != "publish" && code.published != 0) || strings.Contains(w.Body.String(), "private TOS") {
				t.Fatalf("unsafe archive accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestModelEvaluationCodeFreezeUsesTrustedSourceAndMatchesIdentity(t *testing.T) {
	h, store, submit, _, _ := evaluationArchiveTestSetup()
	store.evaluator.Code = &me.CodeSnapshot{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("d", 64), SizeBytes: 123, Format: "zip"}
	store.evaluator.GitURL = ""
	store.evaluator.GitCommit = ""
	w := evaluationTestRequest(evaluationTestRouter(h, streamingPrincipal()), "POST", "/api/v1/model-evaluations", evaluationTestBody())
	if w.Code != 202 {
		t.Fatalf("code evaluation %d %s", w.Code, w.Body.String())
	}
	source := submit.last.Spec.Source
	if source.Type != "evaluation-archive" || source.ArtifactID != store.evaluator.Code.ID || source.ArtifactSHA256 != store.evaluator.Code.SHA256 || source.ArtifactObjectKey != "" || source.URL != "" || source.Commit != "" {
		t.Fatalf("unsafe code source %+v", source)
	}
	e := store.evaluation
	job := &domain.TrainingJob{ID: e.JobID, TenantID: e.TenantID, UserID: e.OwnerID, SubmissionOrigin: domain.SubmissionOriginEvaluation, ExternalSubmissionID: e.ID, Spec: e.JobSpec}
	if !evaluationJobMatches(job, e) {
		t.Fatal("valid archived evaluation not matched")
	}
	job.Spec.Source.ArtifactID = strings.Repeat("e", 32)
	if evaluationJobMatches(job, e) {
		t.Fatal("different archive accepted as same evaluation job")
	}
}
func TestModelEvaluationCodeDownloadUsesOnlyFrozenSnapshot(t *testing.T) {
	h, store, _, code, _ := evaluationArchiveTestSetup()
	content := "immutable archive"
	sum := sha256.Sum256([]byte(content))
	snapshot := me.CodeSnapshot{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(content)), Format: "zip"}
	store.evaluation = me.Evaluation{ID: "evaluation-1", JobID: "job-0123456789abcdef01234567", Evaluator: me.Evaluator{Code: &snapshot}}
	code.content = content
	code.size = int64(len(content))
	r := evaluationTestRouter(h, streamingPrincipal())
	req := httptest.NewRequest("GET", "/api/v1/internal/jobs/"+store.evaluation.JobID+"/model-evaluation/code?codeId=attacker", nil)
	req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || code.opened != 1 || code.code.ID != snapshot.ID || w.Body.String() != content || w.Header().Get("X-Content-SHA256") != snapshot.SHA256 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || !code.reader.closed {
		t.Fatalf("unsafe code download %d %s", w.Code, w.Body.String())
	}
	store.evaluation.JobID = "job-aaaaaaaaaaaaaaaaaaaaaaaa"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 || code.opened != 1 {
		t.Fatalf("cross-job token opened snapshot: %d calls=%d", w.Code, code.opened)
	}
}

type evaluationCodeRegistrationFailureStore struct {
	*evaluationStoreFake
	saveThenError bool
}

func (s *evaluationCodeRegistrationFailureStore) CreateEvaluator(_ context.Context, e me.Evaluator) (me.Evaluator, error) {
	s.createCalls++
	if s.saveThenError {
		s.evaluator = e
	}
	return me.Evaluator{}, errors.New("private database details")
}
func TestModelEvaluatorCodeDatabaseFailureNeverChangesSnapshotIdentity(t *testing.T) {
	for _, committed := range []bool{false, true} {
		h, store, _, code, _ := evaluationArchiveTestSetup()
		h.modelEvaluations = &evaluationCodeRegistrationFailureStore{evaluationStoreFake: store, saveThenError: committed}
		p := streamingPrincipal()
		p.Roles = []string{domain.RoleSuperAdmin}
		r := evaluationTestRouter(h, p)
		w := evaluationTestRequest(r, "POST", "/api/v1/model-evaluators", evaluationArchivePlanBody())
		want := 503
		if committed {
			want = 200
		}
		if w.Code != want || code.published != 1 || strings.Contains(w.Body.String(), "private database") {
			t.Fatalf("database outcome mishandled: committed=%v status=%d %s", committed, w.Code, w.Body.String())
		}
		original := code.publishID
		w = evaluationTestRequest(r, "POST", "/api/v1/model-evaluators", evaluationArchivePlanBody())
		if w.Code != want || code.publishID != original {
			t.Fatalf("retry changed immutable snapshot: %d %s", w.Code, w.Body.String())
		}
		if committed && code.published != 1 {
			t.Fatal("committed registration republished code")
		}
	}
}
func TestModelEvaluatorCodeRequiresStableRequestKey(t *testing.T) {
	h, _, _, code, _ := evaluationArchiveTestSetup()
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleSuperAdmin}
	r := evaluationTestRouter(h, p)
	req := httptest.NewRequest("POST", "/api/v1/model-evaluators", strings.NewReader(evaluationArchivePlanBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 || code.published != 0 {
		t.Fatalf("missing key published archive: %d %s", w.Code, w.Body.String())
	}
}
func TestModelEvaluationCodeConcurrencyGateStopsDiskOperations(t *testing.T) {
	h, _, _, code, _ := evaluationArchiveTestSetup()
	for i := 0; i < 4; i++ {
		h.evaluationCodeOperations <- struct{}{}
	}
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleSuperAdmin}
	w := evaluationTestRequest(evaluationTestRouter(h, p), "POST", "/api/v1/model-evaluators", evaluationArchivePlanBody())
	if w.Code != 429 || code.published != 0 || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("unbounded archive operations: %d %s", w.Code, w.Body.String())
	}
}
func TestModelEvaluationCodeDownloadFailsBeforeHeadersOnSizeMismatch(t *testing.T) {
	h, store, _, code, _ := evaluationArchiveTestSetup()
	snapshot := me.CodeSnapshot{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), SizeBytes: 8, Format: "zip"}
	store.evaluation = me.Evaluation{ID: "evaluation-1", JobID: "job-0123456789abcdef01234567", Evaluator: me.Evaluator{Code: &snapshot}}
	code.content = "bad"
	code.size = 3
	r := evaluationTestRouter(h, streamingPrincipal())
	req := httptest.NewRequest("GET", "/api/v1/internal/jobs/"+store.evaluation.JobID+"/model-evaluation/code", nil)
	req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 || !code.reader.closed || code.reader.bytesRead != 0 || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("invalid archive streamed: %d %s", w.Code, w.Body.String())
	}
}
