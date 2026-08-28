package observability

import (
	"context"
	"fmt"
	"regexp"
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
	expression func(domain.TrainingWorkloadRef) string
}

var trainingPerformanceMetrics = []trainingPerformanceMetric{
	{name: "cpuCores", expression: cadvisorRate("container_cpu_usage_seconds_total", `container!="",container!="POD"`)},
	{name: "memoryWorkingSetBytes", expression: cadvisorSum("container_memory_working_set_bytes", `container!="",container!="POD"`)},
	{name: "networkReceiveBytesPerSecond", expression: cadvisorRate("container_network_receive_bytes_total", "")},
	{name: "networkTransmitBytesPerSecond", expression: cadvisorRate("container_network_transmit_bytes_total", "")},
	{name: "nodeNetworkReceiveBytesPerSecond", expression: nodeRateMetric("node_network_receive_bytes_total")},
	{name: "nodeNetworkTransmitBytesPerSecond", expression: nodeRateMetric("node_network_transmit_bytes_total")},
	{name: "gpuUtilizationPercent", expression: dcgmInstant(gpuMetricNames.utilization)},
	{name: "gpuMemoryUsedMiB", expression: dcgmInstant(gpuMetricNames.memoryUsed)},
	{name: "gpuPowerWatts", expression: dcgmInstant(gpuMetricNames.power)},
	{name: "gpuTemperatureCelsius", expression: dcgmInstant(gpuMetricNames.temperature)},
	{name: "objectStoreBytes", expression: rayInstant("ray_object_store_memory")},
	{name: "objectStoreSpillBytesPerSecond", expression: rayRate("ray_object_store_spilled_bytes_total")},
	{name: "cacheBytes", expression: cacheMetric("ray_cache_bytes", "sum")},
	{name: "cacheHitsPerSecond", expression: cacheMetric("ray_cache_hits_total", "sum")},
	{name: "cacheMissesPerSecond", expression: cacheMetric("ray_cache_misses_total", "sum")},
	{name: "cachePreloaderDurationSeconds", expression: cacheMetric("ray_cache_preloader_duration_seconds", "max")},
	{name: "stepTimeSeconds", expression: rayInstant("ray_platform_training_step_time_seconds")},
	{name: "dataTimeSeconds", expression: rayInstant("ray_platform_training_data_time_seconds")},
	{name: "ncclDurationSeconds", expression: rayInstant("ray_platform_training_nccl_duration_seconds")},
	{name: "step", expression: rayInstant("ray_platform_training_step")},
	{name: "restarts", expression: kubernetesInstant("kube_pod_container_status_restarts_total", `container="ray-worker"`)},
	{name: "state", expression: func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("max by (pod, node, phase) (kube_pod_status_phase%s == 1)", kubernetesSelector(ref, `phase=~"Pending|Running|Succeeded|Failed|Unknown"`))
	}},
}

const trainingPerformanceGroup = "pod, exported_pod, node, rank, worker_rank, UUID, state, phase"

func workerPodRegex(ref domain.TrainingWorkloadRef) string {
	return "^" + regexp.QuoteMeta(ref.RayClusterName) + "-.*-worker-.*$"
}

func selector(labels ...string) string     { return "{" + strings.Join(labels, ",") + "}" }
func exactLabel(name, value string) string { return name + "=" + strconv.Quote(value) }
func regexLabel(name, value string) string { return name + "=~" + strconv.Quote(value) }

func cadvisorSelector(ref domain.TrainingWorkloadRef, extra string) string {
	labels := []string{exactLabel("namespace", ref.Namespace), regexLabel("pod", workerPodRegex(ref))}
	if extra != "" {
		labels = append(labels, extra)
	}
	return selector(labels...)
}

func cadvisorSum(metric, extra string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("sum by (%s) (%s%s)", trainingPerformanceGroup, metric, cadvisorSelector(ref, extra))
	}
}

func cadvisorRate(metric, extra string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("sum by (%s) (rate(%s%s[1m]))", trainingPerformanceGroup, metric, cadvisorSelector(ref, extra))
	}
}

func dcgmInstant(metric string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		scope := selector(exactLabel("exported_namespace", ref.Namespace), regexLabel("exported_pod", workerPodRegex(ref)))
		return fmt.Sprintf("max by (%s) (%s%s)", trainingPerformanceGroup, metric, scope)
	}
}

func raySelector(ref domain.TrainingWorkloadRef) string {
	return selector(exactLabel("exported_namespace", ref.Namespace), exactLabel("ray_io_cluster", ref.RayClusterName), `ray_io_node_type="worker"`)
}

func rayInstant(metric string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("max by (%s) (%s%s)", trainingPerformanceGroup, metric, raySelector(ref))
	}
}

func rayRate(metric string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("sum by (%s) (rate(%s%s[1m]))", trainingPerformanceGroup, metric, raySelector(ref))
	}
}

func cacheMetric(metric, aggregation string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		scope := selector(exactLabel("exported_namespace", ref.Namespace), exactLabel("ray_io_cluster", ref.RayClusterName), exactLabel("platform_job_id", ref.JobID))
		return fmt.Sprintf("%s by (%s) (%s%s)", aggregation, trainingPerformanceGroup, metric, scope)
	}
}

func kubernetesSelector(ref domain.TrainingWorkloadRef, extra string) string {
	labels := []string{exactLabel("namespace", ref.Namespace), regexLabel("pod", workerPodRegex(ref))}
	if extra != "" {
		labels = append(labels, extra)
	}
	return selector(labels...)
}

func kubernetesInstant(metric, extra string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("max by (%s) (%s%s)", trainingPerformanceGroup, metric, kubernetesSelector(ref, extra))
	}
}

func nodeRateMetric(metric string) func(domain.TrainingWorkloadRef) string {
	return func(ref domain.TrainingWorkloadRef) string {
		workers := fmt.Sprintf("max by (node) (kube_pod_info%s)", kubernetesSelector(ref, ""))
		network := fmt.Sprintf(`label_replace(rate(%s{device!~"lo|veth.*|cali.*|flannel.*"}[1m]) * on(instance) group_left(nodename) node_uname_info, "node", "$1", "nodename", "(.*)")`, metric)
		return fmt.Sprintf("sum by (node) ((%s) and on(node) (%s))", network, workers)
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
	workers := map[string]*domain.TrainingWorkerPerformance{}
	workerLatest := map[string]map[string]domain.TrainingMetricPoint{}
	summaryLatest := map[string]domain.TrainingMetricPoint{}
	totalSeries := 0
	for _, metric := range trainingPerformanceMetrics {
		series, err := c.queryRangeWithStep(queryCtx, metric.expression(ref), start, end, spec.step)
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
		{name: "job ID", value: ref.JobID},
		{name: "namespace", value: ref.Namespace, errors: validation.IsDNS1123Label(ref.Namespace)},
		{name: "RayCluster name", value: ref.RayClusterName, errors: validation.IsDNS1123Subdomain(ref.RayClusterName)},
		{name: "RayJob name", value: ref.RayJobName, errors: validation.IsDNS1123Subdomain(ref.RayJobName)},
	}
	for _, item := range values {
		if item.value == "" || len(item.errors) != 0 || strings.IndexFunc(item.value, unicode.IsControl) >= 0 || (item.name == "job ID" && !safeLabelValue(item.value)) {
			return fmt.Errorf("training workload %s is invalid", item.name)
		}
	}
	return nil
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
