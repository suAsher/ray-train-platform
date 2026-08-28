package observability

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/domain"
)

const (
	maxTrainingPerformanceQueries = 24
	maxTrainingPerformanceSeries  = 2048
	maxTrainingPerformancePoints  = 2048
	trainingPerformanceTimeout    = 5 * time.Second
)

type trainingPerformanceWindow struct {
	duration time.Duration
	step     time.Duration
}

var trainingPerformanceWindows = map[string]trainingPerformanceWindow{
	"15m": {duration: 15 * time.Minute, step: 15 * time.Second},
	"1h":  {duration: time.Hour, step: 30 * time.Second},
	"6h":  {duration: 6 * time.Hour, step: time.Minute},
	"24h": {duration: 24 * time.Hour, step: 5 * time.Minute},
}

type trainingPerformanceMetric struct {
	name       string
	expression func(string) string
}

var trainingPerformanceMetrics = []trainingPerformanceMetric{
	{name: "cpuCores", expression: rateMetric("container_cpu_usage_seconds_total")},
	{name: "memoryWorkingSetBytes", expression: instantMetric("container_memory_working_set_bytes")},
	{name: "networkReceiveBytesPerSecond", expression: rateMetric("container_network_receive_bytes_total")},
	{name: "networkTransmitBytesPerSecond", expression: rateMetric("container_network_transmit_bytes_total")},
	{name: "nodeNetworkReceiveBytesPerSecond", expression: nodeRateMetric("node_network_receive_bytes_total")},
	{name: "nodeNetworkTransmitBytesPerSecond", expression: nodeRateMetric("node_network_transmit_bytes_total")},
	{name: "gpuUtilizationPercent", expression: instantMetric(gpuMetricNames.utilization)},
	{name: "gpuMemoryUsedMiB", expression: instantMetric(gpuMetricNames.memoryUsed)},
	{name: "gpuPowerWatts", expression: instantMetric(gpuMetricNames.power)},
	{name: "gpuTemperatureCelsius", expression: instantMetric(gpuMetricNames.temperature)},
	{name: "objectStoreBytes", expression: instantMetric("ray_object_store_memory")},
	{name: "objectStoreSpillBytesPerSecond", expression: rateMetric("ray_object_store_spilled_bytes_total")},
	{name: "cacheBytes", expression: instantMetric("ray_cache_bytes")},
	{name: "cacheHitsPerSecond", expression: rateMetric("ray_cache_hits_total")},
	{name: "cacheMissesPerSecond", expression: rateMetric("ray_cache_misses_total")},
	{name: "cachePreloaderDurationSeconds", expression: instantMetric("ray_cache_preloader_duration_seconds")},
	{name: "stepTimeSeconds", expression: instantMetric("platform_training_step_time_seconds")},
	{name: "dataTimeSeconds", expression: instantMetric("platform_training_data_time_seconds")},
	{name: "ncclDurationSeconds", expression: instantMetric("platform_training_nccl_duration_seconds")},
	{name: "step", expression: instantMetric("platform_training_step")},
	{name: "restarts", expression: instantMetric("kube_pod_container_status_restarts_total")},
	{name: "state", expression: func(selector string) string {
		return fmt.Sprintf("max by (pod, exported_pod, node, rank, worker_rank, UUID, state, phase) (kube_pod_status_phase%s == 1)", selector)
	}},
}

func instantMetric(metric string) func(string) string {
	return func(selector string) string {
		return fmt.Sprintf("max by (pod, exported_pod, node, rank, worker_rank, UUID, state, phase) (%s%s)", metric, selector)
	}
}

func rateMetric(metric string) func(string) string {
	return func(selector string) string {
		return fmt.Sprintf("sum by (pod, exported_pod, node, rank, worker_rank, UUID, state, phase) (rate(%s%s[1m]))", metric, selector)
	}
}

func nodeRateMetric(metric string) func(string) string {
	return func(selector string) string {
		return fmt.Sprintf("sum by (node) (rate(%s%s[1m]))", metric, selector)
	}
}

func (c *PrometheusClient) QueryTrainingPerformance(ctx context.Context, ref domain.TrainingWorkloadRef, window string) (domain.TrainingPerformance, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return domain.TrainingPerformance{}, fmt.Errorf("training performance metrics are not configured")
	}
	if err := validateTrainingWorkloadRef(ref); err != nil {
		return domain.TrainingPerformance{}, err
	}
	spec, ok := trainingPerformanceWindows[window]
	if !ok {
		return domain.TrainingPerformance{}, fmt.Errorf("unsupported training performance window")
	}
	if len(trainingPerformanceMetrics) > maxTrainingPerformanceQueries {
		return domain.TrainingPerformance{}, fmt.Errorf("training performance query budget exceeded")
	}
	end := time.Now().UTC()
	start := end.Add(-spec.duration)
	result := domain.TrainingPerformance{
		Workload: ref, Window: window, StepSeconds: int(spec.step / time.Second), StartedAt: start, EndedAt: end,
		Workers: []domain.TrainingWorkerPerformance{}, Series: map[string][]domain.TrainingMetricSeries{},
		Summary: map[string]*float64{}, Recovery: []domain.TrainingRecoveryPoint{},
	}
	for _, metric := range trainingPerformanceMetrics {
		result.Series[metric.name] = []domain.TrainingMetricSeries{}
		result.Summary[metric.name] = nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, trainingPerformanceTimeout)
	defer cancel()
	selector := trainingPerformanceSelector(ref)
	workers := map[string]*domain.TrainingWorkerPerformance{}
	workerLatest := map[string]map[string]domain.TrainingMetricPoint{}
	summaryLatest := map[string]domain.TrainingMetricPoint{}
	totalSeries := 0
	for _, metric := range trainingPerformanceMetrics {
		series, err := c.queryRangeWithStep(queryCtx, metric.expression(selector), start, end, spec.step)
		if err != nil {
			return domain.TrainingPerformance{}, fmt.Errorf("query training performance metric %s: %w", metric.name, err)
		}
		totalSeries += len(series)
		if totalSeries > maxTrainingPerformanceSeries {
			return domain.TrainingPerformance{}, fmt.Errorf("training performance result contains too many series")
		}
		converted := make([]domain.TrainingMetricSeries, 0, len(series))
		for _, item := range series {
			if len(item.Points) > maxTrainingPerformancePoints {
				return domain.TrainingPerformance{}, fmt.Errorf("training performance series contains too many points")
			}
			points := trainingPoints(item.Points)
			labels := cloneLabels(item.Labels)
			converted = append(converted, domain.TrainingMetricSeries{Labels: labels, Points: points})
			if latest, ok := latestTrainingPoint(points); ok {
				if current, found := summaryLatest[metric.name]; !found || latest.Timestamp.After(current.Timestamp) {
					summaryLatest[metric.name] = latest
					value := latest.Value
					result.Summary[metric.name] = &value
				}
			}
			pod := firstLabel(labels, "pod", "exported_pod")
			if pod == "" {
				continue
			}
			worker := workers[pod]
			if worker == nil {
				worker = newTrainingWorker(pod, labels)
				workers[pod] = worker
				workerLatest[pod] = map[string]domain.TrainingMetricPoint{}
			} else {
				mergeTrainingWorkerLabels(worker, labels)
			}
			worker.Series[metric.name] = append(worker.Series[metric.name], points...)
			if latest, ok := latestTrainingPoint(points); ok {
				if current, found := workerLatest[pod][metric.name]; !found || latest.Timestamp.After(current.Timestamp) {
					workerLatest[pod][metric.name] = latest
					value := latest.Value
					worker.Summary[metric.name] = &value
					if metric.name == "restarts" && latest.Value >= 0 {
						restarts := int(latest.Value)
						worker.Restarts = &restarts
					}
					if metric.name == "step" && latest.Value >= 0 {
						step := int64(latest.Value)
						worker.Step = &step
					}
				}
			}
		}
		result.Series[metric.name] = converted
	}
	for _, worker := range workers {
		result.Workers = append(result.Workers, *worker)
	}
	sort.SliceStable(result.Workers, func(i, j int) bool {
		left, right := result.Workers[i], result.Workers[j]
		if left.Rank != nil && right.Rank != nil && *left.Rank != *right.Rank {
			return *left.Rank < *right.Rank
		}
		if left.Rank != nil && right.Rank == nil {
			return true
		}
		if left.Rank == nil && right.Rank != nil {
			return false
		}
		return left.Pod < right.Pod
	})
	return result, nil
}

func validateTrainingWorkloadRef(ref domain.TrainingWorkloadRef) error {
	values := []struct {
		name, value string
		errors      []string
	}{
		{name: "namespace", value: ref.Namespace, errors: validation.IsDNS1123Label(ref.Namespace)},
		{name: "RayCluster name", value: ref.RayClusterName, errors: validation.IsDNS1123Subdomain(ref.RayClusterName)},
		{name: "RayJob name", value: ref.RayJobName, errors: validation.IsDNS1123Subdomain(ref.RayJobName)},
	}
	for _, item := range values {
		if item.value == "" || len(item.errors) != 0 || strings.IndexFunc(item.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("training workload %s is invalid", item.name)
		}
	}
	return nil
}

func trainingPerformanceSelector(ref domain.TrainingWorkloadRef) string {
	return fmt.Sprintf(`{exported_namespace=%s,ray_io_cluster=%s,ray_io_node_type="worker"}`,
		strconv.Quote(ref.Namespace), strconv.Quote(ref.RayClusterName))
}

func trainingPoints(points []MetricPoint) []domain.TrainingMetricPoint {
	result := make([]domain.TrainingMetricPoint, len(points))
	for index, point := range points {
		result[index] = domain.TrainingMetricPoint{Timestamp: point.Timestamp, Value: point.Value}
	}
	return result
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}

func firstLabel(labels map[string]string, names ...string) string {
	for _, name := range names {
		if value := labels[name]; value != "" {
			return value
		}
	}
	return ""
}

func latestTrainingPoint(points []domain.TrainingMetricPoint) (domain.TrainingMetricPoint, bool) {
	if len(points) == 0 {
		return domain.TrainingMetricPoint{}, false
	}
	latest := points[0]
	for _, point := range points[1:] {
		if point.Timestamp.After(latest.Timestamp) {
			latest = point
		}
	}
	return latest, true
}

func newTrainingWorker(pod string, labels map[string]string) *domain.TrainingWorkerPerformance {
	worker := &domain.TrainingWorkerPerformance{Pod: pod, Series: map[string][]domain.TrainingMetricPoint{}, Summary: map[string]*float64{}}
	for _, metric := range trainingPerformanceMetrics {
		worker.Series[metric.name] = []domain.TrainingMetricPoint{}
		worker.Summary[metric.name] = nil
	}
	mergeTrainingWorkerLabels(worker, labels)
	return worker
}

func mergeTrainingWorkerLabels(worker *domain.TrainingWorkerPerformance, labels map[string]string) {
	if worker.Node == "" {
		worker.Node = firstLabel(labels, "node", "Hostname")
	}
	if worker.GPU == "" {
		worker.GPU = firstLabel(labels, "UUID", "gpu")
	}
	if worker.State == "" {
		worker.State = firstLabel(labels, "state", "phase")
	}
	if worker.Rank != nil {
		return
	}
	if raw := firstLabel(labels, "rank", "worker_rank"); raw != "" {
		if rank, err := strconv.Atoi(raw); err == nil && rank >= 0 {
			worker.Rank = &rank
		}
	}
}
