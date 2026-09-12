package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/repositories"
	"ray-train-platform-backend/trackingartifacts"
)

const artifactRunID = "11111111111111111111111111111111"
const artifactID = "22222222222222222222222222222222"
const artifactPath = "/api/v1/mlflow/runs/" + artifactRunID + "/artifacts"
const artifactSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type artifactAuthorizerFake struct {
	fakeMLflowTrackingService
	authCalls int
	authActor mlflowtracking.Actor
	authWrite bool
	authErr   error
	state     string
}

func (s *artifactAuthorizerFake) AuthorizeArtifact(_ context.Context, a mlflowtracking.Actor, id string, write bool) (mlflowtracking.Actor, mlflowtracking.Run, error) {
	s.authCalls++
	s.authActor = a
	s.authWrite = write
	state := s.state
	if state == "" {
		state = "RUNNING"
	}
	return mlflowtracking.Actor{TenantID: "team-a", UserID: "owner-a"}, mlflowtracking.Run{ID: id, State: state}, s.authErr
}

type artifactServiceFake struct {
	calls    int
	scope    trackingartifacts.Scope
	key      string
	index    int
	sha      string
	body     string
	artifact trackingartifacts.Artifact
	err      error
	reader   io.ReadCloser
}

func (s *artifactServiceFake) mark(scope trackingartifacts.Scope) { s.calls++; s.scope = scope }
func (s *artifactServiceFake) Init(_ context.Context, scope trackingartifacts.Scope, key string, _ trackingartifacts.InitInput) (trackingartifacts.Artifact, error) {
	s.mark(scope)
	s.key = key
	return s.artifact, s.err
}
func (s *artifactServiceFake) List(_ context.Context, scope trackingartifacts.Scope, _ string, _ int) (trackingartifacts.Page, error) {
	s.mark(scope)
	return trackingartifacts.Page{Items: []trackingartifacts.Artifact{s.artifact}}, s.err
}
func (s *artifactServiceFake) Get(_ context.Context, scope trackingartifacts.Scope, _ string) (trackingartifacts.Artifact, error) {
	s.mark(scope)
	return s.artifact, s.err
}
func (s *artifactServiceFake) PutPart(_ context.Context, scope trackingartifacts.Scope, _ string, index int, sha string, body io.Reader) (trackingartifacts.Artifact, error) {
	s.mark(scope)
	s.index = index
	s.sha = sha
	b, err := io.ReadAll(body)
	s.body = string(b)
	if err != nil {
		return trackingartifacts.Artifact{}, err
	}
	return s.artifact, s.err
}
func (s *artifactServiceFake) Complete(_ context.Context, scope trackingartifacts.Scope, _ string) (trackingartifacts.Artifact, error) {
	s.mark(scope)
	return s.artifact, s.err
}
func (s *artifactServiceFake) Cancel(_ context.Context, scope trackingartifacts.Scope, _ string) (trackingartifacts.Artifact, error) {
	s.mark(scope)
	return s.artifact, s.err
}
func (s *artifactServiceFake) Download(_ context.Context, scope trackingartifacts.Scope, _ string) (trackingartifacts.Artifact, io.ReadCloser, error) {
	s.mark(scope)
	return s.artifact, s.reader, s.err
}
func artifactPrincipal() auth.Principal {
	return trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeArtifactsRead, domain.PATScopeArtifactsWrite)
}
func artifactRouter(p auth.Principal, authorize mlflowTrackingService, store trackingArtifactService, audit MLflowDashboardStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&fakeJobRepository{}, Options{MLflowTracking: authorize, TrackingArtifacts: store, MLflowDashboardStore: audit})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	h.RegisterTrainingRoutes(r.Group("/api/v1"))
	return r
}
func artifactRequest(r http.Handler, method, path, body string, length int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.ContentLength = length
	req.Header.Set("Idempotency-Key", "artifact-init-key")
	req.Header.Set("X-Content-SHA256", artifactSHA)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}
func TestMLflowArtifactAuthorizationPrecedesStorage(t *testing.T) {
	for _, methodPath := range [][2]string{{"GET", artifactPath}, {"POST", artifactPath}, {"GET", artifactPath + "/" + artifactID}, {"PUT", artifactPath + "/" + artifactID + "/parts/1"}, {"POST", artifactPath + "/" + artifactID + "/complete"}, {"DELETE", artifactPath + "/" + artifactID}, {"GET", artifactPath + "/" + artifactID + "/content"}} {
		t.Run(methodPath[0]+methodPath[1], func(t *testing.T) {
			a := &artifactAuthorizerFake{authErr: mlflowtracking.ErrNotFound}
			s := &artifactServiceFake{}
			r := artifactRouter(artifactPrincipal(), a, s, newFakeMLflowDashboardStore())
			res := artifactRequest(r, methodPath[0], methodPath[1], "", 0)
			if res.Code != 404 || s.calls != 0 || a.authCalls != 1 {
				t.Fatalf("authorization ordering: status=%d auth=%d storage=%d body=%s", res.Code, a.authCalls, s.calls, res.Body.String())
			}
		})
	}
}
func TestMLflowArtifactScopesAndPATRequired(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		p            auth.Principal
	}{{"missing read", "GET", trackingPrincipal()}, {"missing write", "POST", trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeArtifactsRead)}, {"missing paired read", "POST", trackingPrincipal(domain.PATScopeExperimentsRead, domain.PATScopeArtifactsWrite)}, {"browser write", "POST", auth.Principal{Subject: "user-a", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &artifactAuthorizerFake{}
			s := &artifactServiceFake{}
			res := artifactRequest(artifactRouter(tc.p, a, s, newFakeMLflowDashboardStore()), tc.method, artifactPath, "", 0)
			if res.Code != 403 || s.calls != 0 || a.authCalls != 0 {
				t.Fatalf("scope guard failed: status=%d auth=%d storage=%d", res.Code, a.authCalls, s.calls)
			}
		})
	}
	p := artifactPrincipal()
	p.AuthType = auth.AuthTypeOIDC
	p.Scopes = nil
	a := &artifactAuthorizerFake{}
	s := &artifactServiceFake{}
	res := artifactRequest(artifactRouter(p, a, s, nil), "GET", artifactPath, "", 0)
	if res.Code != 200 || s.calls != 1 {
		t.Fatalf("interactive read failed: %d %s", res.Code, res.Body.String())
	}
}
func TestMLflowArtifactGrantedOwnerAndAuditIdentity(t *testing.T) {
	p := artifactPrincipal()
	p.Subject = "machine-a"
	p.IntegrationID = "integration-a"
	a := &artifactAuthorizerFake{}
	s := &artifactServiceFake{}
	audit := newFakeMLflowDashboardStore()
	body := `{"name":"model.bin","sizeBytes":4,"sha256":"` + artifactSHA + `"}`
	res := artifactRequest(artifactRouter(p, a, s, audit), "POST", artifactPath, body, int64(len(body)))
	if res.Code != 201 || s.scope.OwnerID != "owner-a" || s.scope.RunID != artifactRunID || s.scope.TenantID != "team-a" || a.authActor.IntegrationID != "integration-a" || s.key == "artifact-init-key" {
		t.Fatalf("grant scope/namespace: status=%d scope=%+v actor=%+v key=%q body=%s", res.Code, s.scope, a.authActor, s.key, res.Body.String())
	}
	if len(audit.audits) != 2 || audit.audits[0].Principal.Subject != "machine-a" || audit.audits[0].Principal.IntegrationID != "integration-a" || audit.audits[0].Action != repositories.MLflowAuditTrackingArtifactWrite {
		t.Fatalf("audit lost machine identity: %+v", audit.audits)
	}
}
func TestMLflowArtifactTerminalRunOnlyAllowsCancellation(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", artifactPath, 409}, {"PUT", artifactPath + "/" + artifactID + "/parts/1", 409}, {"POST", artifactPath + "/" + artifactID + "/complete", 409}, {"DELETE", artifactPath + "/" + artifactID, 200}} {
		a := &artifactAuthorizerFake{state: "FINISHED"}
		s := &artifactServiceFake{}
		res := artifactRequest(artifactRouter(artifactPrincipal(), a, s, newFakeMLflowDashboardStore()), tc.method, tc.path, "", 0)
		if res.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, res.Code, res.Body.String())
		}
		if tc.status == 409 && s.calls != 0 {
			t.Fatal("terminal mutation reached storage")
		}
	}
}
func TestMLflowArtifactPartValidationAndStreamingLimit(t *testing.T) {
	for _, tc := range []struct {
		name, part, sha, body string
		length                int64
		status                int
	}{{"bad part", "0", artifactSHA, "", 0, 400}, {"bad hash", "1", "invalid", "abcd", 4, 400}, {"declared oversize", "1", artifactSHA, "", trackingartifacts.PartSizeBytes + 1, 413}, {"declared mismatch", "1", artifactSHA, "abcd", 3, 400}, {"chunked oversize", "1", artifactSHA, strings.Repeat("x", int(trackingartifacts.PartSizeBytes+1)), -1, 413}, {"valid", "1", artifactSHA, "abcd", 4, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &artifactAuthorizerFake{}
			s := &artifactServiceFake{artifact: trackingartifacts.Artifact{SizeBytes: 4, TotalParts: 1}}
			r := artifactRouter(artifactPrincipal(), a, s, newFakeMLflowDashboardStore())
			req := httptest.NewRequest("PUT", artifactPath+"/"+artifactID+"/parts/"+tc.part, strings.NewReader(tc.body))
			req.ContentLength = tc.length
			req.Header.Set("X-Content-SHA256", tc.sha)
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", out.Code, tc.status, out.Body.String())
			}
			if tc.status == 200 && (s.index != 1 || s.body != "abcd") {
				t.Fatalf("part not forwarded: %+v", s)
			}
		})
	}
}
func TestMLflowArtifactDownloadHeadersAndAudit(t *testing.T) {
	reader := &countingReadCloser{reader: strings.NewReader("model-bytes")}
	s := &artifactServiceFake{artifact: trackingartifacts.Artifact{Name: "模型.bin", SizeBytes: 11, State: "READY", SHA256: artifactSHA}, reader: reader}
	audit := newFakeMLflowDashboardStore()
	res := artifactRequest(artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, s, audit), "GET", artifactPath+"/"+artifactID+"/content", "", 0)
	if res.Code != 200 || res.Body.String() != "model-bytes" || !reader.closed || res.Header().Get("Content-Type") != "application/octet-stream" || res.Header().Get("X-Content-Type-Options") != "nosniff" || res.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("unsafe download: %d headers=%v body=%s", res.Code, res.Header(), res.Body.String())
	}
	if len(audit.audits) != 2 || audit.audits[0].Status != 102 || audit.audits[1].Status != 200 || audit.audits[1].Action != repositories.MLflowAuditTrackingArtifactDownload {
		t.Fatalf("download not audited: %+v", audit.audits)
	}
}
func TestMLflowArtifactAuditUnavailablePreventsDownload(t *testing.T) {
	audit := newFakeMLflowDashboardStore()
	audit.auditErr = errors.New("private db failure")
	s := &artifactServiceFake{}
	res := artifactRequest(artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, s, audit), "GET", artifactPath+"/"+artifactID+"/content", "", 0)
	if res.Code != 503 || s.calls != 0 || strings.Contains(res.Body.String(), "private") {
		t.Fatalf("unaudited download: %d calls=%d body=%s", res.Code, s.calls, res.Body.String())
	}
}
func TestMLflowArtifactReadRateLimit(t *testing.T) {
	r := artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, &artifactServiceFake{}, nil)
	for i := 0; i < 120; i++ {
		if res := artifactRequest(r, "GET", artifactPath, "", 0); res.Code != 200 {
			t.Fatalf("attempt %d=%d", i, res.Code)
		}
	}
	res := artifactRequest(r, "GET", artifactPath, "", 0)
	if res.Code != 429 || res.Header().Get("Retry-After") != "60" {
		t.Fatalf("limiter failed: %d", res.Code)
	}
}

type artifactDownloadAuditFake struct {
	*fakeMLflowDashboardStore
	failCompletion bool
}

func (s *artifactDownloadAuditFake) CreateMLflowAuditLog(ctx context.Context, event repositories.MLflowAuditEvent) error {
	if s.failCompletion && event.Status != 102 {
		return errors.New("audit completion unavailable")
	}
	return s.fakeMLflowDashboardStore.CreateMLflowAuditLog(ctx, event)
}

type artifactBrokenReader struct{ sent bool }

func (r *artifactBrokenReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("private object URL")
	}
	r.sent = true
	return copy(p, "part"), nil
}
func (r *artifactBrokenReader) Close() error { return nil }
func TestMLflowArtifactDownloadFailureNeverAppendsJSON(t *testing.T) {
	for _, failAudit := range []bool{false, true} {
		var reader io.ReadCloser = &artifactBrokenReader{}
		size := int64(10)
		if failAudit {
			reader = io.NopCloser(strings.NewReader("part"))
			size = 4
		}
		s := &artifactServiceFake{artifact: trackingartifacts.Artifact{Name: "model.bin", SizeBytes: size, State: "READY"}, reader: reader}
		audit := &artifactDownloadAuditFake{fakeMLflowDashboardStore: newFakeMLflowDashboardStore(), failCompletion: failAudit}
		res := artifactRequest(artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, s, audit), "GET", artifactPath+"/"+artifactID+"/content", "", 0)
		if res.Body.String() != "part" {
			t.Fatalf("stream mixed with error response: %q", res.Body.String())
		}
	}
}
func TestMLflowArtifactPaginationAndIDsAreBounded(t *testing.T) {
	for _, path := range []string{artifactPath + "?cursor=foreign/path", artifactPath + "?limit=0", artifactPath + "?limit=invalid", artifactPath + "/not-hex"} {
		s := &artifactServiceFake{}
		res := artifactRequest(artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, s, nil), "GET", path, "", 0)
		if res.Code != 400 || s.calls != 0 {
			t.Fatalf("invalid query reached storage %s: %d %s", path, res.Code, res.Body.String())
		}
	}
}
func TestMLflowArtifactErrorsAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{trackingartifacts.ErrInvalid, 400, "ARTIFACT_INVALID_REQUEST"}, {trackingartifacts.ErrNotFound, 404, "ARTIFACT_NOT_FOUND"}, {trackingartifacts.ErrConflict, 409, "ARTIFACT_CONFLICT"}, {trackingartifacts.ErrQuota, 409, "ARTIFACT_QUOTA_EXCEEDED"}, {trackingartifacts.ErrUnavailable, 503, "ARTIFACT_UNAVAILABLE"}, {errors.New("private object credential"), 503, "ARTIFACT_UNAVAILABLE"}} {
		s := &artifactServiceFake{err: tc.err}
		res := artifactRequest(artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, s, nil), "GET", artifactPath, "", 0)
		if res.Code != tc.status || !strings.Contains(res.Body.String(), tc.code) || strings.Contains(res.Body.String(), "private") {
			t.Fatalf("unsafe error status=%d body=%s", res.Code, res.Body.String())
		}
	}
}
func TestMLflowArtifactWriteRateLimitAndJSONLimit(t *testing.T) {
	r := artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, &artifactServiceFake{}, newFakeMLflowDashboardStore())
	for i := 0; i < 60; i++ {
		res := artifactRequest(r, "DELETE", artifactPath+"/"+artifactID, "", 0)
		if res.Code != 200 {
			t.Fatalf("write attempt %d=%d %s", i, res.Code, res.Body.String())
		}
	}
	res := artifactRequest(r, "DELETE", artifactPath+"/"+artifactID, "", 0)
	if res.Code != 429 {
		t.Fatalf("write limiter failed %d", res.Code)
	}
	s := &artifactServiceFake{}
	body := `{"name":"` + strings.Repeat("x", mlflowTrackingBodyLimit) + `"}`
	res = artifactRequest(artifactRouter(artifactPrincipal(), &artifactAuthorizerFake{}, s, newFakeMLflowDashboardStore()), "POST", artifactPath, body, int64(len(body)))
	if res.Code != 413 || s.calls != 0 {
		t.Fatalf("JSON limit failed %d calls=%d", res.Code, s.calls)
	}
}

// This writer exercises ResponseController through Gin's Unwrap without a
// sleeping client or a network connection. The existing recorder tests cover
// writers that do not implement deadline support.
type artifactDeadlineWriter struct {
 *httptest.ResponseRecorder
 deadlines []time.Time
 deadlineDuringWrite time.Time
 setErr error
}
func (w *artifactDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
 w.deadlines = append(w.deadlines, deadline)
 return w.setErr
}
func (w *artifactDeadlineWriter) Write(body []byte) (int, error) {
 if len(w.deadlines) > 0 { w.deadlineDuringWrite = w.deadlines[len(w.deadlines)-1] }
 return w.ResponseRecorder.Write(body)
}
func TestMLflowArtifactDownloadWriteDeadlineAndReset(t *testing.T) {
 for _,broken := range []bool{false,true} {
  var reader io.ReadCloser = io.NopCloser(strings.NewReader("part"))
  if broken { reader = &artifactBrokenReader{} }
  s := &artifactServiceFake{artifact:trackingartifacts.Artifact{Name:"model.bin",SizeBytes:4,State:"READY"},reader:reader}
  r := artifactRouter(artifactPrincipal(),&artifactAuthorizerFake{},s,newFakeMLflowDashboardStore())
  req := httptest.NewRequest("GET",artifactPath+"/"+artifactID+"/content",nil)
  before := time.Now()
  w := &artifactDeadlineWriter{ResponseRecorder:httptest.NewRecorder()}
  r.ServeHTTP(w,req)
  if len(w.deadlines)!=2 || !w.deadlines[1].IsZero() || w.deadlines[0].Before(before.Add(14*time.Minute)) || w.deadlines[0].After(time.Now().Add(15*time.Minute)) || !w.deadlineDuringWrite.Equal(w.deadlines[0]) {
   t.Fatalf("download deadline not active/reset: broken=%v calls=%v during=%v",broken,w.deadlines,w.deadlineDuringWrite)
  }
  if w.Body.String()!="part" { t.Fatalf("unexpected attachment: %q",w.Body.String()) }
 }
}
func TestMLflowArtifactDownloadWriteDeadlinePreservesEarlierContext(t *testing.T) {
 s := &artifactServiceFake{artifact:trackingartifacts.Artifact{Name:"model.bin",SizeBytes:4,State:"READY"},reader:io.NopCloser(strings.NewReader("part"))}
 r := artifactRouter(artifactPrincipal(),&artifactAuthorizerFake{},s,newFakeMLflowDashboardStore())
 deadline := time.Now().Add(time.Minute)
 ctx,cancel := context.WithDeadline(context.Background(),deadline);defer cancel()
 req := httptest.NewRequest("GET",artifactPath+"/"+artifactID+"/content",nil).WithContext(ctx)
 w := &artifactDeadlineWriter{ResponseRecorder:httptest.NewRecorder()}
 r.ServeHTTP(w,req)
 if len(w.deadlines)!=2 || !w.deadlines[0].Equal(deadline) || !w.deadlines[1].IsZero() { t.Fatalf("request deadline lost: %v",w.deadlines) }
}
func TestMLflowArtifactDownloadWriteDeadlineFailureStopsStream(t *testing.T) {
 reader := &countingReadCloser{reader:strings.NewReader("part")}
 s := &artifactServiceFake{artifact:trackingartifacts.Artifact{Name:"model.bin",SizeBytes:4,State:"READY"},reader:reader}
 r := artifactRouter(artifactPrincipal(),&artifactAuthorizerFake{},s,newFakeMLflowDashboardStore())
 req := httptest.NewRequest("GET",artifactPath+"/"+artifactID+"/content",nil)
 w := &artifactDeadlineWriter{ResponseRecorder:httptest.NewRecorder(),setErr:errors.New("socket deadline failure")}
 r.ServeHTTP(w,req)
 if w.Code!=503 || len(w.deadlines)!=1 || reader.bytesRead!=0 || !reader.closed { t.Fatalf("failed deadline still streamed: status=%d calls=%v read=%d closed=%v",w.Code,w.deadlines,reader.bytesRead,reader.closed) }
}
