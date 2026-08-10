package observability

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestQueryJobMetricsReadsKnownTrainingMetrics(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Path != "/api/v1/query_range" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if !strings.Contains(request.URL.Query().Get("query"), `platform_job_id="job-1"`) {
			t.Fatalf("unexpected query: %s", request.URL.Query().Get("query"))
		}
		body := `{"status":"success","data":{"result":[{"values":[["1000","1.5"],["1015","1.25"]]}]}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}
	metrics, err := client.QueryJobMetrics(context.Background(), "job-1", 0)
	if err != nil {
		t.Fatalf("query metrics: %v", err)
	}
	if requests != len(jobMetricQueries) || metrics.Loss == nil || *metrics.Loss != 1.25 || metrics.Epoch == nil {
		t.Fatalf("unexpected metrics: requests=%d metrics=%+v", requests, metrics)
	}
}

func TestQueryJobMetricsRejectsUnsafeJobID(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus"}
	if _, err := client.QueryJobMetrics(context.Background(), `job-1"}`, 0); err == nil {
		t.Fatal("expected unsafe job id validation error")
	}
}
