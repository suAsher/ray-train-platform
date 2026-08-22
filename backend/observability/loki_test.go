package observability

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestQueryJobLogsReadsAndSortsLokiStreams(t *testing.T) {
	client := LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if request.URL.Query().Get("query") != `{platform_job_id="job-1"}` {
			t.Fatalf("unexpected query: %s", request.URL.Query().Get("query"))
		}
		if request.URL.Query().Get("start") == "" || request.URL.Query().Get("end") == "" {
			t.Fatalf("Loki query must include an explicit retention window: %s", request.URL.RawQuery)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"result":[{"stream":{"pod":"worker-1"},"values":[["2000000000","second"],["1000000000","first"]]}]}}`)), Request: request}, nil
	})}}
	lines, err := client.QueryJobLogs(context.Background(), "job-1", 10)
	if err != nil {
		t.Fatalf("query logs: %v", err)
	}
	if len(lines) != 2 || lines[0].Line != "first" || !lines[0].Timestamp.Equal(time.Unix(1, 0).UTC()) {
		t.Fatalf("unexpected logs: %+v", lines)
	}
}

func TestQueryJobLogsInRangeUsesRequestedLifecycleWindow(t *testing.T) {
	start := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	end := start.Add(17 * time.Minute)
	client := LokiClient{BaseURL: "http://loki", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotStart, err := time.Parse(time.RFC3339Nano, request.URL.Query().Get("start"))
		if err != nil || !gotStart.Equal(start) {
			t.Fatalf("unexpected start: %q (%v)", request.URL.Query().Get("start"), err)
		}
		gotEnd, err := time.Parse(time.RFC3339Nano, request.URL.Query().Get("end"))
		if err != nil || !gotEnd.Equal(end) {
			t.Fatalf("unexpected end: %q (%v)", request.URL.Query().Get("end"), err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"result":[]}}`)), Request: request}, nil
	})}}
	if _, err := client.QueryJobLogsInRange(context.Background(), "job-1", 10, start, end); err != nil {
		t.Fatalf("query logs in range: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestQueryJobLogsRejectsUnsafeLabelValue(t *testing.T) {
	client := LokiClient{BaseURL: "http://loki"}
	if _, err := client.QueryJobLogs(context.Background(), `job-1"}`, 10); err == nil {
		t.Fatal("expected unsafe job id validation error")
	}
}
