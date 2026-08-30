package observability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func prometheusMatrixResponse(request *http.Request, result string) *http.Response {
	body := `{"status":"success","data":{"result":` + result + `}}`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestTrainingPerformanceKeepsRanksInOnePodAndPrefersExportedPod(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		name := metricNameFromExpression(request.URL.Query().Get("query"))
		switch name {
		case "ray_platform_training_step_time_seconds":
			return prometheusMatrixResponse(request, `[
				{"metric":{"pod":"cluster-a-worker-shared","rank":"0","gpu":"0"},"values":[[1000,"4"]]},
				{"metric":{"pod":"cluster-a-worker-shared","rank":"1","gpu":"1"},"values":[[1000,"6"]]}
			]`), nil
		case "DCGM_FI_DEV_GPU_UTIL":
			return prometheusMatrixResponse(request, `[
				{"metric":{"pod":"dcgm-exporter-x","exported_pod":"cluster-a-worker-shared","gpu":"0","UUID":"GPU-A"},"values":[[1000,"40"]]},
				{"metric":{"pod":"dcgm-exporter-x","exported_pod":"cluster-a-worker-shared","gpu":"1","UUID":"GPU-B"},"values":[[1000,"60"]]}
			]`), nil
		case "ray_cache_bytes":
			return prometheusMatrixResponse(request, `[{"metric":{"pod":"cache-exporter-x","exported_pod":"cluster-a-worker-shared"},"values":[[1000,"12"]]}]`), nil
		default:
			return prometheusMatrixResponse(request, `[]`), nil
		}
	})}}

	got, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 2 {
		t.Fatalf("same-pod ranks collapsed or exporter row leaked: %+v", got.Workers)
	}
	for index, worker := range got.Workers {
		if worker.Pod != "cluster-a-worker-shared" || worker.Rank == nil || *worker.Rank != index {
			t.Fatalf("unexpected worker %d: %+v", index, worker)
		}
		wantGPU := []string{"GPU-A", "GPU-B"}[index]
		if worker.GPU != wantGPU {
			t.Fatalf("rank %d GPU = %q, want %q", index, worker.GPU, wantGPU)
		}
	}
}

func TestTrainingPerformanceDoesNotMergeRankWithUnrelatedLocalGPUOrdinal(t *testing.T) {
	tests := []struct {
		name    string
		workers [][2]string
	}{
		{name: "global ranks differ from local ordinals", workers: [][2]string{{"8", "0"}, {"9", "1"}}},
		{name: "rank and GPU ordinals cross", workers: [][2]string{{"0", "1"}, {"1", "0"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			series := make([]domain.TrainingMetricSeries, 0, len(test.workers))
			for _, identity := range test.workers {
				series = append(series, domain.TrainingMetricSeries{
					Labels: map[string]string{"pod": "cluster-a-worker-shared", "rank": identity[0], "gpu": identity[1]},
					Points: []domain.TrainingMetricPoint{{Timestamp: time.Unix(1000, 0).UTC(), Value: 1}},
				})
			}

			got := assembleTrainingWorkers(map[string][]domain.TrainingMetricSeries{"stepTimeSeconds": series})

			if len(got) != len(test.workers) {
				t.Fatalf("rank/GPU identities collapsed: got %d workers, want %d: %+v", len(got), len(test.workers), got)
			}
			seen := map[string]string{}
			for _, assembly := range got {
				if assembly.worker.Rank == nil {
					t.Fatalf("worker has no rank: %+v", assembly.worker)
				}
				seen[strconv.Itoa(*assembly.worker.Rank)] = assembly.worker.GPU
			}
			for _, identity := range test.workers {
				if seen[identity[0]] != identity[1] {
					t.Fatalf("rank %s GPU = %q, want %q; workers=%+v", identity[0], seen[identity[0]], identity[1], got)
				}
			}
		})
	}
}

func TestTrainingPerformancePartialFailuresAreBoundedAndDeterministic(t *testing.T) {
	var active, maximum int32
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		current := atomic.AddInt32(&active, 1)
		for {
			seen := atomic.LoadInt32(&maximum)
			if current <= seen || atomic.CompareAndSwapInt32(&maximum, seen, current) {
				break
			}
		}
		defer atomic.AddInt32(&active, -1)
		time.Sleep(10 * time.Millisecond)
		name := metricNameFromExpression(request.URL.Query().Get("query"))
		if name == "DCGM_FI_DEV_POWER_USAGE" || name == "ray_cache_misses_total" {
			return nil, errors.New("dial http://prometheus.internal bearer-secret")
		}
		return prometheusMatrixResponse(request, `[]`), nil
	})}}

	got, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	if err != nil {
		t.Fatal(err)
	}
	if maximum < 2 || maximum > 5 {
		t.Fatalf("query concurrency = %d, want 2..5", maximum)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	want := []any{"gpuPowerWatts", "cacheMissesTotal"}
	if !reflect.DeepEqual(payload["unavailableMetrics"], want) {
		t.Fatalf("unavailable metrics = %#v, want %#v", payload["unavailableMetrics"], want)
	}
}

func TestTrainingPerformanceAllMetricFailuresReturnGenericError(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("dial http://prometheus.internal bearer-secret")
	})}}
	_, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	if err == nil || err.Error() != "training performance metrics unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrainingPerformanceSummaryUsesExplicitReducers(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		name := metricNameFromExpression(request.URL.Query().Get("query"))
		values := map[string][2]string{
			"ray_platform_training_step_time_seconds":                   {"4", "6"},
			"DCGM_FI_DEV_GPU_UTIL":                                      {"40", "60"},
			"ray_cache_bytes":                                           {"10", "20"},
			"ray_platform_training_step":                                {"7", "9"},
			"ray_cache_preloader_duration_seconds":                      {"1", "3"},
			"ray_platform_training_dataset_prefetch_wait_seconds_total": {"2", "4"},
			"ray_platform_training_dataset_cache_hits_total":            {"8", "12"},
		}
		pair, ok := values[name]
		if !ok {
			return prometheusMatrixResponse(request, `[]`), nil
		}
		result := `[{"metric":{"pod":"cluster-a-worker-a","rank":"0","gpu":"0"},"values":[[1000,"` + pair[0] + `"]]},` +
			`{"metric":{"pod":"cluster-a-worker-b","rank":"1","gpu":"1"},"values":[[1000,"` + pair[1] + `"]]}]`
		return prometheusMatrixResponse(request, result), nil
	})}}
	got, err := client.QueryTrainingPerformance(context.Background(), domain.TrainingWorkloadRef{JobID: "job-a", Namespace: "tenant-a", RayClusterName: "cluster-a", RayJobName: "job-a"}, "15m")
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]float64{"stepTimeSeconds": 5, "gpuUtilizationPercent": 50, "cacheBytes": 30, "step": 9, "cachePreloaderDurationSeconds": 3, "datasetPrefetchWaitSecondsTotal": 6, "datasetCacheHitsTotal": 20}
	for name, want := range wants {
		if got.Summary[name] == nil || *got.Summary[name] != want {
			t.Fatalf("summary %s = %v, want %v", name, got.Summary[name], want)
		}
	}
}

func TestTrainingPerformanceUsesMetricFamilySpecificProductionSelectors(t *testing.T) {
	requests := 0
	queries := map[string]string{}
	badStep := ""
	var mutex sync.Mutex
	client := PrometheusClient{BaseURL: "http://prometheus.internal", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		defer mutex.Unlock()
		requests++
		expression := request.URL.Query().Get("query")
		queries[metricNameFromExpression(expression)] = expression
		if request.URL.Query().Get("step") != "30" {
			badStep = request.URL.RawQuery
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
	if badStep != "" {
		t.Fatalf("unexpected step: %s", badStep)
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
	for _, name := range []string{"ray_platform_training_step", "ray_platform_training_step_time_seconds", "ray_platform_training_data_time_seconds", "ray_platform_training_nccl_duration_seconds", "ray_platform_training_dataset_prefetch_wait_seconds_total", "ray_platform_training_dataset_source_read_seconds_total", "ray_platform_training_dataset_cache_read_seconds_total", "ray_platform_training_dataset_cache_hits_total", "ray_platform_training_dataset_cache_checksum_failures_total", "ray_platform_training_dataset_cache_stale_temp_reclaimed_total", "ray_object_store_memory"} {
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
		"ray_platform_training_dataset_prefetch_wait_seconds_total", "ray_platform_training_dataset_source_read_seconds_total", "ray_platform_training_dataset_cache_read_seconds_total",
		"ray_platform_training_dataset_batches_total", "ray_platform_training_dataset_samples_total", "ray_platform_training_dataset_shard_reads_total",
		"ray_platform_training_dataset_source_reads_total", "ray_platform_training_dataset_cache_reads_total", "ray_platform_training_dataset_cache_hits_total",
		"ray_platform_training_dataset_cache_misses_total", "ray_platform_training_dataset_cache_downloads_total", "ray_platform_training_dataset_cache_fallbacks_total", "ray_platform_training_dataset_cache_checksum_failures_total",
		"ray_platform_training_dataset_cache_evictions_total", "ray_platform_training_dataset_cache_stale_temp_reclaimed_total", "ray_platform_training_dataset_cache_bytes_total",
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
