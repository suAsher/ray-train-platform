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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestQueryJobLogsRejectsUnsafeLabelValue(t *testing.T) {
	client := LokiClient{BaseURL: "http://loki"}
	if _, err := client.QueryJobLogs(context.Background(), `job-1"}`, 10); err == nil {
		t.Fatal("expected unsafe job id validation error")
	}
}
