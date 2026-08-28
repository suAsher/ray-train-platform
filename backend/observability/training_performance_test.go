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

func TestTrainingPerformanceUsesMetricFamilySpecificProductionSelectors(t *testing.T) {
	requests := 0
	queries := map[string]string{}
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		expression := request.URL.Query().Get("query")
		queries[metricNameFromExpression(expression)] = expression
		if request.URL.Query().Get("step") != "30" {
			t.Fatalf("unexpected step: %s", request.URL.RawQuery)
		}
		body := `{"status":"success","data":{"result":[]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}

	performance, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{
		JobID: "job-a", Namespace: "tenant-a", RayClusterName: "job-a-cluster", RayJobName: "job-a",
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
	workerRegex := `pod=~"^job-a-cluster-.*-worker-.*$"`
	for _, name := range []string{"container_cpu_usage_seconds_total", "container_memory_working_set_bytes", "container_network_receive_bytes_total", "container_network_transmit_bytes_total"} {
		expression := queries[name]
		if !strings.Contains(expression, `namespace="tenant-a"`) || !strings.Contains(expression, workerRegex) || strings.Contains(expression, "ray_io_cluster") {
			t.Fatalf("cAdvisor query is not pod-scoped through Kubernetes identity: %s", expression)
		}
	}
	for _, name := range []string{"DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED"} {
		expression := queries[name]
		if !strings.Contains(expression, `exported_namespace="tenant-a"`) || !strings.Contains(expression, `exported_pod=~"^job-a-cluster-.*-worker-.*$"`) {
			t.Fatalf("DCGM query is not exported-pod scoped: %s", expression)
		}
	}
	for _, name := range []string{"ray_platform_training_step", "ray_platform_training_step_time_seconds", "ray_platform_training_data_time_seconds", "ray_platform_training_nccl_duration_seconds", "ray_object_store_memory"} {
		expression := queries[name]
		for _, selector := range []string{`exported_namespace="tenant-a"`, `ray_io_cluster="job-a-cluster"`, `ray_io_node_type="worker"`} {
			if !strings.Contains(expression, selector) {
				t.Fatalf("Ray query %s missing %s: %s", name, selector, expression)
			}
		}
	}
	cache := queries["ray_cache_bytes"]
	for _, selector := range []string{`exported_namespace="tenant-a"`, `ray_io_cluster="job-a-cluster"`, `platform_job_id="job-a"`} {
		if !strings.Contains(cache, selector) {
			t.Fatalf("cache query missing %s: %s", selector, cache)
		}
	}
	for _, name := range []string{"ray_cache_hits_total", "ray_cache_misses_total"} {
		if strings.Contains(queries[name], "rate(") {
			t.Fatalf("cumulative cache counter was converted to a sparse rate: %s", queries[name])
		}
	}
	node := queries["node_network_receive_bytes_total"]
	if !strings.Contains(node, "kube_pod_info") || !strings.Contains(node, `namespace="tenant-a"`) || !strings.Contains(node, workerRegex) || strings.Contains(node, `node_network_receive_bytes_total{exported_namespace`) {
		t.Fatalf("node network query does not use worker-node join: %s", node)
	}
	for _, name := range []string{"kube_pod_status_phase", "kube_pod_container_status_restarts_total"} {
		if !strings.Contains(queries[name], `namespace="tenant-a"`) || !strings.Contains(queries[name], workerRegex) {
			t.Fatalf("Kubernetes query is not persisted-pod scoped: %s", queries[name])
		}
	}
}

func metricNameFromExpression(expression string) string {
	for _, name := range []string{
		"container_cpu_usage_seconds_total", "container_memory_working_set_bytes", "container_network_receive_bytes_total", "container_network_transmit_bytes_total",
		"node_network_receive_bytes_total", "node_network_transmit_bytes_total", "DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_POWER_USAGE", "DCGM_FI_DEV_GPU_TEMP",
		"ray_object_store_memory", "ray_object_store_spilled_bytes_total", "ray_cache_bytes", "ray_cache_hits_total", "ray_cache_misses_total", "ray_cache_preloader_duration_seconds",
		"ray_platform_training_step_time_seconds", "ray_platform_training_data_time_seconds", "ray_platform_training_nccl_duration_seconds", "ray_platform_training_step",
		"kube_pod_container_status_restarts_total", "kube_pod_status_phase",
	} {
		if strings.Contains(expression, name) {
			return name
		}
	}
	return expression
}

func TestTrainingPerformanceRejectsUnsafePersistedLabelsBeforeQuery(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, context.Canceled
	})}}
	cases := []domain.TrainingWorkloadRef{
		{JobID: "job-a", Namespace: "", RayClusterName: "cluster-a", RayJobName: "job-a"},
		{JobID: "job-a", Namespace: "tenant-a\n", RayClusterName: "cluster-a", RayJobName: "job-a"},
		{JobID: "job-a", Namespace: "tenant-a", RayClusterName: `cluster-a"} or vector(1)`, RayJobName: "job-a"},
		{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a\x00evil"},
		{JobID: `job-a"}`, Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"},
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
		if _, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "7d"); err == nil {
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
		_, _ = client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	})
	t.Run("results", func(t *testing.T) {
		series := strings.Repeat(`{"metric":{"pod":"p"},"values":[[1000,"1"]]},`, maxTrainingPerformanceSeries+1)
		series = strings.TrimSuffix(series, ",")
		client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := `{"status":"success","data":{"result":[` + series + `]}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})}}
		_, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
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
	got, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
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
