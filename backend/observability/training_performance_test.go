package observability

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestTrainingPerformanceQueriesAreScopedToPersistedWorkerWorkload(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		expression := request.URL.Query().Get("query")
		for _, selector := range []string{`exported_namespace="tenant-a"`, `ray_io_cluster="job-a-cluster"`, `ray_io_node_type="worker"`} {
			if !strings.Contains(expression, selector) {
				t.Fatalf("query missing persisted worker selector %s: %s", selector, expression)
			}
		}
		if request.URL.Query().Get("step") != "30" {
			t.Fatalf("unexpected step: %s", request.URL.RawQuery)
		}
		body := `{"status":"success","data":{"result":[]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}

	performance, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{
		Namespace: "tenant-a", RayClusterName: "job-a-cluster", RayJobName: "job-a",
	}, "1h")
	if err != nil {
		t.Fatal(err)
	}
	if requests == 0 || requests > maxTrainingPerformanceQueries {
		t.Fatalf("query count outside bounds: %d", requests)
	}
	if performance.StepSeconds != 30 || performance.Window != "1h" || performance.Workers == nil || performance.Series == nil {
		t.Fatalf("unexpected immutable empty response: %+v", performance)
	}
	for name, value := range performance.Summary {
		if value != nil {
			t.Fatalf("missing metric %s became zero/value %v", name, *value)
		}
	}
}

func TestTrainingPerformanceRejectsUnsafePersistedLabelsBeforeQuery(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, context.Canceled
	})}}
	cases := []domain.TrainingWorkloadRef{
		{Namespace: "", RayClusterName: "cluster-a", RayJobName: "job-a"},
		{Namespace: "tenant-a\n", RayClusterName: "cluster-a", RayJobName: "job-a"},
		{Namespace: "tenant-a", RayClusterName: `cluster-a"} or vector(1)`, RayJobName: "job-a"},
		{Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a\x00evil"},
	}
	for _, ref := range cases {
		if _, err := client.QueryTrainingPerformance(context.Background(), ref, "1h"); err == nil {
			t.Fatalf("unsafe workload ref accepted: %#v", ref)
		}
	}
	if requests != 0 {
		t.Fatalf("unsafe labels made %d requests", requests)
	}
}

func TestTrainingPerformanceBoundsWindowTimeoutAndResultCount(t *testing.T) {
	t.Run("window", func(t *testing.T) {
		client := PrometheusClient{BaseURL: "http://prometheus.internal"}
		if _, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "7d"); err == nil {
			t.Fatal("unbounded window accepted")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			deadline, ok := request.Context().Deadline()
			if !ok || time.Until(deadline) > trainingPerformanceTimeout+time.Second {
				t.Fatalf("query has no bounded deadline: %v %v", deadline, ok)
			}
			return nil, context.DeadlineExceeded
		})}}
		_, _ = client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	})
	t.Run("results", func(t *testing.T) {
		series := strings.Repeat(`{"metric":{"pod":"p"},"values":[[1000,"1"]]},`, maxTrainingPerformanceSeries+1)
		series = strings.TrimSuffix(series, ",")
		client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := `{"status":"success","data":{"result":[` + series + `]}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})}}
		_, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
		if err == nil || strings.Contains(err.Error(), "prometheus.internal") {
			t.Fatalf("result bound not enforced or URL leaked: %v", err)
		}
	})
}

func TestTrainingPerformanceBuildsWorkerAndSummaryWithoutAliasing(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		expression := request.URL.Query().Get("query")
		value := "2"
		if strings.Contains(expression, "step_time") {
			value = "4.5"
		}
		body := `{"status":"success","data":{"result":[{"metric":{"pod":"cluster-a-worker-abc","node":"node-a","rank":"0","UUID":"GPU-1","state":"RUNNING"},"values":[[1000,"` + value + `"]]}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}
	got, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 1 || got.Workers[0].Rank == nil || *got.Workers[0].Rank != 0 || got.Workers[0].Pod != "cluster-a-worker-abc" || got.Workers[0].GPU != "GPU-1" {
		t.Fatalf("worker identity not assembled: %+v", got.Workers)
	}
	if got.Summary["stepTimeSeconds"] == nil || strconv.FormatFloat(*got.Summary["stepTimeSeconds"], 'f', 1, 64) != "4.5" {
		t.Fatalf("summary missing latest step time: %+v", got.Summary)
	}
}
