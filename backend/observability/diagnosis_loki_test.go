package observability

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDiagnosisLokiCandidatesAreFilteredForwardBoundedAndInjectionSafe(t *testing.T) {
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	calls := 0
	client := &LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		query := request.URL.Query()
		if !strings.HasPrefix(query.Get("query"), `{platform_job_id="job-1"} |~ `) || query.Get("direction") != "forward" || query.Get("limit") != "200" { t.Fatalf("unbounded diagnosis query: %s", request.URL.String()) }
		for _, marker := range []string{"Error", "NCCL", "nan"} { if !strings.Contains(query.Get("query"), marker) { t.Fatalf("missing %s filter: %s", marker, query.Get("query")) } }
		if strings.Contains(strings.ToLower(query.Get("query")), "checkpoint") { t.Fatal("routine checkpoints must not starve failure candidate budget") }
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"result":[]}}`)), Header: make(http.Header), Request: request}, nil
	})}}
	provider, ok := any(client).(interface { QueryJobDiagnosisCandidates(context.Context, string, int, time.Time, time.Time) ([]LogLine, error) })
	if !ok { t.Fatal("Loki client lacks filtered diagnosis candidates") }
	if _, err := provider.QueryJobDiagnosisCandidates(context.Background(), "job-1", 200, start, start.Add(time.Hour)); err != nil { t.Fatal(err) }
	if _, err := provider.QueryJobDiagnosisCandidates(context.Background(), `job-1"} |= "`, 200, start, start.Add(time.Hour)); err == nil { t.Fatal("injected job label accepted") }
	if calls != 1 { t.Fatalf("unsafe job reached Loki: %d calls", calls) }
}

func TestDiagnosisLokiCompletionEvidenceUsesSeparateBackwardBudget(t *testing.T) {
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	client := &LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("direction") != "backward" || query.Get("limit") != "20" || !strings.Contains(strings.ToLower(query.Get("query")), "checkpoint") { t.Fatalf("completion query: %s", request.URL.String()) }
		if query.Get("end") != start.Add(time.Hour).Format(time.RFC3339Nano) { t.Fatal("completion queried beyond failure") }
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"result":[]}}`)), Header: make(http.Header), Request: request}, nil
	})}}
	provider, ok := any(client).(interface { QueryJobDiagnosisCompletions(context.Context, string, int, time.Time, time.Time) ([]LogLine, error) })
	if !ok { t.Fatal("Loki client lacks separate completion evidence query") }
	if _, err := provider.QueryJobDiagnosisCompletions(context.Background(), "job-1", 20, start, start.Add(time.Hour)); err != nil { t.Fatal(err) }
}

func TestDiagnosisLokiContextQuotesExactStreamAndRespectsDeadline(t *testing.T) {
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	pod := `worker"} |~ "injected`
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := &LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		want := `{platform_job_id="job-1",pod=` + strconv.Quote(pod) + `,container="trainer"}`
		if query.Get("query") != want || query.Get("limit") != "12" || query.Get("direction") != "backward" { t.Fatalf("context query escaped isolation: %s", request.URL.String()) }
		if query.Get("start") != start.Format(time.RFC3339Nano) || query.Get("end") != start.Add(time.Minute).Format(time.RFC3339Nano) { t.Fatal("context range changed") }
		if _, ok := request.Context().Deadline(); !ok { t.Fatal("query lost request deadline") }
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"result":[]}}`)), Header: make(http.Header), Request: request}, nil
	})}}
	if _, err := client.QueryJobDiagnosisContext(ctx, "job-1", 999, start, start.Add(time.Minute), LogDirectionBackward, map[string]string{"pod": pod, "container": "trainer", "arbitrary": "ignored"}); err != nil { t.Fatal(err) }
}

func TestDiagnosisLokiFollowupsHaveSeparateBackwardBudget(t *testing.T) {
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	client := &LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("limit") != "12" || query.Get("direction") != "backward" || !strings.Contains(query.Get("query"), "NCCL") || strings.Contains(query.Get("query"), "checkpoint") { t.Fatalf("followups query: %s", request.URL.String()) }
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"result":[]}}`)), Header: make(http.Header), Request: request}, nil
	})}}
	if _, err := client.QueryJobDiagnosisFollowups(context.Background(), "job-1", 999, start, start.Add(48*time.Hour)); err != nil { t.Fatal(err) }
}

func TestDiagnosisLokiRejectsOversizedResponse(t *testing.T) {
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	client := &LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", diagnosisLokiResponseBytes+1))), Header: make(http.Header), Request: request}, nil
	})}}
	if _, err := client.QueryJobDiagnosisCandidates(context.Background(), "job-1", 200, start, start.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "size limit") { t.Fatalf("expected response cap: %v", err) }
}
