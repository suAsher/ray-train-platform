package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type diagnosisTestProvider struct {
	candidates   []observability.LogLine
	context      []observability.LogLine
	err          error
	contextErr   error
	calls        int
	contextCalls int
	limit        int
	start, end   time.Time
	deadline     time.Time
}

func (p *diagnosisTestProvider) QueryJobLogs(context.Context, string, int) ([]observability.LogLine, error) {
	panic("diagnosis must never fall back to a full-log query")
}

func (p *diagnosisTestProvider) QueryJobDiagnosisCandidates(ctx context.Context, _ string, limit int, start, end time.Time) ([]observability.LogLine, error) {
	p.calls++
	p.limit, p.start, p.end = limit, start, end
	p.deadline, _ = ctx.Deadline()
	return p.candidates, p.err
}

func (p *diagnosisTestProvider) QueryJobLogsPage(_ context.Context, _ string, limit int, start, end time.Time, direction observability.LogDirection) ([]observability.LogLine, error) {
	p.contextCalls++
	if limit > 24 || end.Sub(start) > 2*time.Minute {
		panic("diagnosis context query is not bounded")
	}
	return p.context, p.contextErr
}

type diagnosisTestEvidence struct {
	Timestamp time.Time               `json:"timestamp"`
	Line      string                  `json:"line"`
	Kind      string                  `json:"kind"`
	Context   []observability.LogLine `json:"context"`
}

type diagnosisTestPayload struct {
	JobID              string                  `json:"jobId"`
	ObservedState      string                  `json:"observedState"`
	Classification     string                  `json:"classification"`
	Summary            string                  `json:"summary"`
	FirstFailure       *diagnosisTestEvidence  `json:"firstFailure"`
	Followups          []diagnosisTestEvidence `json:"followups"`
	CompletionEvidence []diagnosisTestEvidence `json:"completionEvidence"`
	Coverage           struct {
		Filtered           bool `json:"filtered"`
		Partial            bool `json:"partial"`
		Truncated          bool `json:"truncated"`
		LogUnavailable     bool `json:"logUnavailable"`
		ContextUnavailable bool `json:"contextUnavailable"`
	} `json:"coverage"`
}

func diagnosisTestJob() domain.TrainingJob {
	return domain.TrainingJob{ID: "job-01", TenantID: "team-a", ObservedState: domain.StateRunning, CreatedAt: time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)}
}

func readDiagnosisTestResponse(t *testing.T, repository *fakeJobRepository, provider LogProvider) (diagnosisTestPayload, *httptest.ResponseRecorder) {
	t.Helper()
	response := httptest.NewRecorder()
	logTestRouter(repository, provider).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/diagnosis", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("diagnosis status=%d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data diagnosisTestPayload `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data, response
}

func TestJobDiagnosisPreservesEarliestFailureBeforeCommunicationFollowups(t *testing.T) {
	for _, first := range []string{"loss=nan", "KeyError: 'RANK'", "torch.OutOfMemoryError: CUDA out of memory"} {
		t.Run(first, func(t *testing.T) {
			job := diagnosisTestJob()
			repository := &fakeJobRepository{jobs: []domain.TrainingJob{job}}
			provider := &diagnosisTestProvider{candidates: []observability.LogLine{
				{Timestamp: job.CreatedAt.Add(3 * time.Second), Line: "NCCL watchdog timeout"},
				{Timestamp: job.CreatedAt.Add(time.Second), Line: first},
			}}
			data, response := readDiagnosisTestResponse(t, repository, provider)
			if data.FirstFailure == nil || data.FirstFailure.Line != first || len(data.Followups) != 1 {
				t.Fatalf("unexpected evidence: %+v", data)
			}
			if data.ObservedState != "RUNNING" || !reflect.DeepEqual(repository.jobs[0], job) || repository.canceled != "" {
				t.Fatal("diagnosis changed job state")
			}
			if !data.Coverage.Filtered || !data.Coverage.Partial || provider.calls != 1 || provider.limit > 200 {
				t.Fatalf("unbounded coverage/provider: %+v %+v", data, provider)
			}
			remaining := time.Until(provider.deadline)
			if remaining <= 0 || remaining > 8*time.Second {
				t.Fatalf("missing bounded deadline: %v", remaining)
			}
			if !provider.start.Equal(job.CreatedAt.Add(-10*time.Minute)) || provider.contextCalls > 2 {
				t.Fatalf("unexpected window/context: %+v", provider)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("diagnosis must not be cached")
			}
		})
	}
}

func TestJobDiagnosisDoesNotSkipEarlierCommunicationFailure(t *testing.T) {
	job := diagnosisTestJob()
	provider := &diagnosisTestProvider{candidates: []observability.LogLine{
		{Timestamp: job.CreatedAt, Line: "NCCL watchdog timeout"},
		{Timestamp: job.CreatedAt.Add(time.Second), Line: "KeyError: 'secondary'"},
	}}
	data, _ := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, provider)
	if data.FirstFailure == nil || data.FirstFailure.Kind != "communication_error" {
		t.Fatalf("earliest error lost: %+v", data)
	}
}

func TestJobDiagnosisCompletionBeforeSegfaultIsUnverifiedPossibleTeardown(t *testing.T) {
	job := diagnosisTestJob()
	provider := &diagnosisTestProvider{candidates: []observability.LogLine{
		{Timestamp: job.CreatedAt, Line: "Training completed"},
		{Timestamp: job.CreatedAt.Add(time.Second), Line: "Saving checkpoint to /output/final.pth"},
		{Timestamp: job.CreatedAt.Add(2 * time.Second), Line: "Fatal Python error: Segmentation fault"},
	}}
	data, _ := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, provider)
	if data.Classification != "possible_runtime_teardown" || len(data.CompletionEvidence) != 2 || data.ObservedState != "RUNNING" {
		t.Fatalf("completion promoted or lost: %+v", data)
	}
}

func TestJobDiagnosisBenignLinesDoNotBecomeFailureEvidence(t *testing.T) {
	job := diagnosisTestJob()
	provider := &diagnosisTestProvider{}
	for _, line := range []string{"NCCL INFO Channel 00", "nan_check=False", "loss=0.02", "except KeyError:", "caught KeyError: optional metric missing; continuing", "Traceback (most recent call last):", "fatal_error_count=0"} {
		provider.candidates = append(provider.candidates, observability.LogLine{Timestamp: job.CreatedAt, Line: line})
	}
	data, _ := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, provider)
	if data.FirstFailure != nil || data.Classification != "no_failure_evidence" || provider.contextCalls != 0 {
		t.Fatalf("benign sample promoted: %+v", data)
	}
}

func TestJobDiagnosisUnavailableAndContextFailuresAreExplicit(t *testing.T) {
	for _, test := range []struct {
		name                            string
		provider                        LogProvider
		unavailable, contextUnavailable bool
	}{
		{"missing", nil, true, false},
		{"unsupported", &pagedLogProvider{}, true, false},
		{"loki-error", &diagnosisTestProvider{err: errors.New("private backend details")}, true, false},
		{"timeout", &diagnosisTestProvider{err: context.DeadlineExceeded}, true, false},
		{"no-events", &diagnosisTestProvider{}, false, false},
		{"context-error", &diagnosisTestProvider{candidates: []observability.LogLine{{Timestamp: diagnosisTestJob().CreatedAt, Line: "KeyError: 'RANK'"}}, contextErr: context.DeadlineExceeded}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, response := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{diagnosisTestJob()}}, test.provider)
			if data.Coverage.LogUnavailable != test.unavailable || data.Coverage.ContextUnavailable != test.contextUnavailable {
				t.Fatalf("coverage: %+v", data)
			}
			if strings.Contains(response.Body.String(), "private backend") {
				t.Fatal("provider details leaked")
			}
		})
	}
}

func TestJobDiagnosisRedactsAndBoundsEvidence(t *testing.T) {
	job := diagnosisTestJob()
	line := "KeyError: token=synthetic-secret Authorization: Bearer synthetic-bearer https://bucket/key?X-Amz-Signature=synthetic-signature\x1b[31m " + strings.Repeat("x", 6000)
	provider := &diagnosisTestProvider{}
	for index := 0; index < 250; index++ {
		entry := observability.LogLine{Timestamp: job.CreatedAt.Add(time.Duration(index) * time.Second), Line: line, Stream: map[string]string{"pod": "worker-1", "token": "synthetic-label-secret"}}
		provider.candidates = append(provider.candidates, entry)
		provider.context = append(provider.context, entry)
	}
	data, response := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, provider)
	if !data.Coverage.Truncated || data.FirstFailure == nil || len(data.FirstFailure.Context) > 24 || response.Body.Len() >= 1<<20 {
		t.Fatalf("unbounded response: %d bytes, %+v", response.Body.Len(), data.Coverage)
	}
	for _, secret := range []string{"synthetic-secret", "synthetic-bearer", "synthetic-signature", "synthetic-label-secret", `\u001b`} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
	if provider.candidates[0].Line != line || provider.candidates[0].Stream["token"] != "synthetic-label-secret" {
		t.Fatal("redaction mutated provider source")
	}
}

func TestJobDiagnosisAuthorizationPrecedesProviderAccess(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal *auth.Principal
		want      int
	}{
		{"anonymous", nil, http.StatusUnauthorized},
		{"wrong-scope", &auth.Principal{Subject: "user", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}, http.StatusForbidden},
		{"other-team", &auth.Principal{Subject: "user", TenantID: "team-b", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, http.StatusNotFound},
		{"read-scope", &auth.Principal{Subject: "user", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &diagnosisTestProvider{}
			router := gin.New()
			if test.principal != nil {
				router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", *test.principal); c.Next() })
			}
			NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{diagnosisTestJob()}}, Options{Logs: provider}).RegisterTrainingRoutes(router.Group("/api/v1"))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/diagnosis", nil))
			if response.Code != test.want {
				t.Fatalf("got %d want %d", response.Code, test.want)
			}
			if test.want != http.StatusOK && provider.calls != 0 {
				t.Fatal("unauthorized request queried logs")
			}
		})
	}
}

type diagnosisSupplementTestProvider struct {
	diagnosisTestProvider
	completionEnd   time.Time
	followupStart   time.Time
	completionError error
	followupError   error
}

func (p *diagnosisSupplementTestProvider) QueryJobDiagnosisCompletions(_ context.Context, _ string, limit int, start, end time.Time) ([]observability.LogLine, error) {
	p.completionEnd = end
	if limit > 20 {
		panic("unbounded completions")
	}
	return []observability.LogLine{{Timestamp: start.Add(time.Second), Line: "Saving checkpoint to epoch_1.pth"}}, p.completionError
}

func (p *diagnosisSupplementTestProvider) QueryJobDiagnosisFollowups(_ context.Context, _ string, limit int, start, end time.Time) ([]observability.LogLine, error) {
	p.followupStart = start
	if limit > 12 {
		panic("unbounded followups")
	}
	return []observability.LogLine{{Timestamp: start.Add(25 * time.Hour), Line: "NCCL watchdog timeout"}}, p.followupError
}

func TestJobDiagnosisKeepsDayLaterFollowupOutsideFirstCandidateBudget(t *testing.T) {
	job := diagnosisTestJob()
	job.CreatedAt = job.CreatedAt.Add(-72 * time.Hour)
	provider := &diagnosisSupplementTestProvider{}
	for index := 0; index < 200; index++ {
		provider.candidates = append(provider.candidates, observability.LogLine{Timestamp: job.CreatedAt.Add(time.Duration(index) * time.Second), Line: "KeyError: Qtractor"})
	}
	data, _ := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, provider)
	if data.FirstFailure == nil || len(data.Followups) != 1 || !data.Coverage.Truncated || !provider.completionEnd.Equal(data.FirstFailure.Timestamp) || !provider.followupStart.Equal(data.FirstFailure.Timestamp) {
		t.Fatalf("followup lost/budget wrong: %+v", data)
	}
}
