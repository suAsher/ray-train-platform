package observability

import (
	"context"
	"io"
	"net/http"
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
