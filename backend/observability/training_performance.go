package observability

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/domain"
)

const (
	maxTrainingPerformanceQueries  = 48
	maxTrainingPerformanceSeries   = 2048
	maxTrainingPerformancePoints   = 2048
	trainingPerformanceTimeout     = 5 * time.Second
	trainingPerformanceConcurrency = 5
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
	reducer    trainingSummaryReducer
}

type trainingSummaryReducer uint8

const (
	trainingSummaryAverage trainingSummaryReducer = iota
	trainingSummarySum
	trainingSummaryMax
)

var trainingPerformanceMetrics = []trainingPerformanceMetric{
	{name: "cpuCores", expression: cadvisorRate("container_cpu_usage_seconds_total", `container!="",container!="POD"`), reducer: trainingSummarySum},
	{name: "memoryWorkingSetBytes", expression: cadvisorSum("container_memory_working_set_bytes", `container!="",container!="POD"`), reducer: trainingSummarySum},
	{name: "networkReceiveBytesPerSecond", expression: cadvisorRate("container_network_receive_bytes_total", ""), reducer: trainingSummarySum},
	{name: "networkTransmitBytesPerSecond", expression: cadvisorRate("container_network_transmit_bytes_total", ""), reducer: trainingSummarySum},
	{name: "nodeNetworkReceiveBytesPerSecond", expression: nodeRateMetric("node_network_receive_bytes_total"), reducer: trainingSummarySum},
	{name: "nodeNetworkTransmitBytesPerSecond", expression: nodeRateMetric("node_network_transmit_bytes_total"), reducer: trainingSummarySum},
	{name: "gpuUtilizationPercent", expression: dcgmInstant(gpuMetricNames.utilization), reducer: trainingSummaryAverage},
	{name: "gpuMemoryUsedMiB", expression: dcgmInstant(gpuMetricNames.memoryUsed), reducer: trainingSummaryAverage},
	{name: "gpuPowerWatts", expression: dcgmInstant(gpuMetricNames.power), reducer: trainingSummaryAverage},
	{name: "gpuTemperatureCelsius", expression: dcgmInstant(gpuMetricNames.temperature), reducer: trainingSummaryAverage},
	{name: "objectStoreBytes", expression: rayInstant("ray_object_store_memory"), reducer: trainingSummarySum},
	{name: "objectStoreSpillBytesPerSecond", expression: rayRate("ray_object_store_spilled_bytes_total"), reducer: trainingSummarySum},
	{name: "cacheBytes", expression: cacheMetric("ray_cache_bytes", "sum"), reducer: trainingSummarySum},
	{name: "cacheHitsTotal", expression: cacheMetric("ray_cache_hits_total", "sum"), reducer: trainingSummarySum},
	{name: "cacheMissesTotal", expression: cacheMetric("ray_cache_misses_total", "sum"), reducer: trainingSummarySum},
	{name: "cachePreloaderDurationSeconds", expression: cacheMetric("ray_cache_preloader_duration_seconds", "max"), reducer: trainingSummaryMax},
	{name: "stepTimeSeconds", expression: rayInstant("ray_platform_training_step_time_seconds"), reducer: trainingSummaryAverage},
	{name: "dataTimeSeconds", expression: rayInstant("ray_platform_training_data_time_seconds"), reducer: trainingSummaryAverage},
	{name: "ncclDurationSeconds", expression: rayInstant("ray_platform_training_nccl_duration_seconds"), reducer: trainingSummaryAverage},
	{name: "step", expression: rayInstant("ray_platform_training_step"), reducer: trainingSummaryMax},
	{name: "datasetPrefetchWaitSecondsTotal", expression: rayInstant("ray_platform_training_dataset_prefetch_wait_seconds_total"), reducer: trainingSummarySum},
	{name: "datasetSourceReadSecondsTotal", expression: rayInstant("ray_platform_training_dataset_source_read_seconds_total"), reducer: trainingSummarySum},
	{name: "datasetCacheReadSecondsTotal", expression: rayInstant("ray_platform_training_dataset_cache_read_seconds_total"), reducer: trainingSummarySum},
	{name: "datasetSourceReadP95Seconds", expression: rayInstant("ray_platform_training_dataset_source_read_p95_seconds"), reducer: trainingSummaryMax},
	{name: "datasetCacheReadP95Seconds", expression: rayInstant("ray_platform_training_dataset_cache_read_p95_seconds"), reducer: trainingSummaryMax},
	{name: "datasetPrefetchWaitP95Seconds", expression: rayInstant("ray_platform_training_dataset_prefetch_wait_p95_seconds"), reducer: trainingSummaryMax},
	{name: "datasetBatchesTotal", expression: rayInstant("ray_platform_training_dataset_batches_total"), reducer: trainingSummarySum},
	{name: "datasetSamplesTotal", expression: rayInstant("ray_platform_training_dataset_samples_total"), reducer: trainingSummarySum},
	{name: "datasetShardReadsTotal", expression: rayInstant("ray_platform_training_dataset_shard_reads_total"), reducer: trainingSummarySum},
	{name: "datasetSourceReadsTotal", expression: rayInstant("ray_platform_training_dataset_source_reads_total"), reducer: trainingSummarySum},
	{name: "datasetCacheReadsTotal", expression: rayInstant("ray_platform_training_dataset_cache_reads_total"), reducer: trainingSummarySum},
	{name: "datasetSourceBytesTotal", expression: rayInstant("ray_platform_training_dataset_source_bytes_total"), reducer: trainingSummarySum},
	{name: "datasetCacheBytesReadTotal", expression: rayInstant("ray_platform_training_dataset_cache_bytes_read_total"), reducer: trainingSummarySum},
	{name: "datasetCacheHitsTotal", expression: rayInstant("ray_platform_training_dataset_cache_hits_total"), reducer: trainingSummarySum},
	{name: "datasetCacheMissesTotal", expression: rayInstant("ray_platform_training_dataset_cache_misses_total"), reducer: trainingSummarySum},
	{name: "datasetCacheDownloadsTotal", expression: rayInstant("ray_platform_training_dataset_cache_downloads_total"), reducer: trainingSummarySum},
	{name: "datasetCacheFallbacksTotal", expression: rayInstant("ray_platform_training_dataset_cache_fallbacks_total"), reducer: trainingSummarySum},
	{name: "datasetCacheChecksumFailuresTotal", expression: rayInstant("ray_platform_training_dataset_cache_checksum_failures_total"), reducer: trainingSummarySum},
	{name: "datasetCacheEvictionsTotal", expression: rayInstant("ray_platform_training_dataset_cache_evictions_total"), reducer: trainingSummarySum},
	{name: "datasetCacheStaleTempReclaimedTotal", expression: rayInstant("ray_platform_training_dataset_cache_stale_temp_reclaimed_total"), reducer: trainingSummarySum},
	{name: "datasetCacheBytesTotal", expression: rayInstant("ray_platform_training_dataset_cache_bytes_total"), reducer: trainingSummarySum},
	{name: "restarts", expression: kubernetesInstant("kube_pod_container_status_restarts_total", `container="ray-worker"`), reducer: trainingSummaryMax},
	{name: "state", expression: func(ref domain.TrainingWorkloadRef) string {
		return fmt.Sprintf("max by (pod, node, phase) (kube_pod_status_phase%s == 1)", kubernetesSelector(ref, `phase=~"Pending|Running|Succeeded|Failed|Unknown"`))
	}, reducer: trainingSummaryMax},
}

const trainingPerformanceGroup = "pod, exported_pod, node, rank, worker_rank, gpu, UUID, state, phase, dataset_id, dataset_version_id, ray_version, data_mode, cache_policy"

type trainingMetricQueryResult struct {
	metric trainingPerformanceMetric
	series []MetricSeries
	err    error
}

type trainingWorkerAssembly struct {
	worker       *domain.TrainingWorkerPerformance
	latestValues map[string][]float64
}

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
		Summary: map[string]*float64{}, UnavailableMetrics: []string{}, Recovery: []domain.TrainingRecoveryPoint{},
	}
	for _, metric := range trainingPerformanceMetrics {
		result.Series[metric.name] = []domain.TrainingMetricSeries{}
		result.Summary[metric.name] = nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, trainingPerformanceTimeout)
	defer cancel()
	queryResults := c.queryTrainingMetrics(queryCtx, ref, start, end, spec.step)
	successfulQueries := 0
	totalSeries := 0
	for _, queryResult := range queryResults {
		if queryResult.err != nil {
			result.UnavailableMetrics = append(result.UnavailableMetrics, queryResult.metric.name)
			continue
		}
		successfulQueries++
		totalSeries += len(queryResult.series)
		if totalSeries > maxTrainingPerformanceSeries {
			return domain.TrainingPerformance{}, fmt.Errorf("training performance result contains too many series")
		}
		converted := make([]domain.TrainingMetricSeries, 0, len(queryResult.series))
		for _, item := range queryResult.series {
			if len(item.Points) > maxTrainingPerformancePoints {
				return domain.TrainingPerformance{}, fmt.Errorf("training performance series contains too many points")
			}
			points := trainingPoints(item.Points)
			labels := cloneLabels(item.Labels)
			converted = append(converted, domain.TrainingMetricSeries{Labels: labels, Points: points})
		}
		result.Series[queryResult.metric.name] = converted
		result.Summary[queryResult.metric.name] = reduceTrainingMetric(queryResult.metric.reducer, converted)
	}
	if successfulQueries == 0 {
		return domain.TrainingPerformance{}, fmt.Errorf("training performance metrics unavailable")
	}

	workers := assembleTrainingWorkers(result.Series)
	for _, worker := range workers {
		result.Workers = append(result.Workers, *worker.worker)
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
		if left.Pod != right.Pod {
			return left.Pod < right.Pod
		}
		return left.GPU < right.GPU
	})
	return result, nil
}

func (c *PrometheusClient) queryTrainingMetrics(ctx context.Context, ref domain.TrainingWorkloadRef, start, end time.Time, step time.Duration) []trainingMetricQueryResult {
	results := make([]trainingMetricQueryResult, len(trainingPerformanceMetrics))
	semaphore := make(chan struct{}, trainingPerformanceConcurrency)
	var wait sync.WaitGroup
	for index, metric := range trainingPerformanceMetrics {
		wait.Add(1)
		go func(index int, metric trainingPerformanceMetric) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index] = trainingMetricQueryResult{metric: metric, err: ctx.Err()}
				return
			}
			series, err := c.queryRangeWithStep(ctx, metric.expression(ref), start, end, step)
			results[index] = trainingMetricQueryResult{metric: metric, series: series, err: err}
		}(index, metric)
	}
	wait.Wait()
	return results
}

func reduceTrainingMetric(reducer trainingSummaryReducer, series []domain.TrainingMetricSeries) *float64 {
	values := make([]float64, 0, len(series))
	for _, item := range series {
		latest, ok := latestTrainingPoint(item.Points)
		if ok && !math.IsNaN(latest.Value) && !math.IsInf(latest.Value, 0) {
			values = append(values, latest.Value)
		}
	}
	if len(values) == 0 {
		return nil
	}
	value := values[0]
	switch reducer {
	case trainingSummarySum:
		for _, candidate := range values[1:] {
			value += candidate
		}
	case trainingSummaryMax:
		for _, candidate := range values[1:] {
			if candidate > value {
				value = candidate
			}
		}
	default:
		for _, candidate := range values[1:] {
			value += candidate
		}
		value /= float64(len(values))
	}
	return &value
}

func assembleTrainingWorkers(seriesByMetric map[string][]domain.TrainingMetricSeries) []*trainingWorkerAssembly {
	workers := make([]*trainingWorkerAssembly, 0)
	aliases := map[string]*trainingWorkerAssembly{}
	byPod := map[string][]*trainingWorkerAssembly{}

	for _, metric := range trainingPerformanceMetrics {
		for _, item := range seriesByMetric[metric.name] {
			pod := preferredTrainingPod(item.Labels)
			identity := trainingWorkerAliases(pod, item.Labels)
			if pod == "" || len(identity) == 0 {
				continue
			}
			worker := findTrainingWorker(aliases, identity)
			if worker == nil {
				worker = &trainingWorkerAssembly{worker: newTrainingWorker(pod, item.Labels), latestValues: map[string][]float64{}}
				workers = append(workers, worker)
				byPod[pod] = append(byPod[pod], worker)
			}
			registerTrainingWorkerAliases(aliases, worker, identity)
			mergeTrainingWorkerLabels(worker.worker, item.Labels)
		}
	}

	for _, metric := range trainingPerformanceMetrics {
		for _, item := range seriesByMetric[metric.name] {
			pod := preferredTrainingPod(item.Labels)
			if pod == "" {
				continue
			}
			identity := trainingWorkerAliases(pod, item.Labels)
			targets := make([]*trainingWorkerAssembly, 0, 1)
			if len(identity) > 0 {
				if worker := findTrainingWorker(aliases, identity); worker != nil {
					targets = append(targets, worker)
				}
			} else {
				targets = append(targets, byPod[pod]...)
			}
			if len(targets) == 0 {
				worker := &trainingWorkerAssembly{worker: newTrainingWorker(pod, item.Labels), latestValues: map[string][]float64{}}
				workers = append(workers, worker)
				byPod[pod] = append(byPod[pod], worker)
				registerTrainingWorkerAliases(aliases, worker, identity)
				targets = append(targets, worker)
			}
			for _, target := range targets {
				mergeTrainingWorkerLabels(target.worker, item.Labels)
				target.worker.Series[metric.name] = append(target.worker.Series[metric.name], item.Points...)
				if latest, ok := latestTrainingPoint(item.Points); ok && !math.IsNaN(latest.Value) && !math.IsInf(latest.Value, 0) {
					target.latestValues[metric.name] = append(target.latestValues[metric.name], latest.Value)
				}
			}
		}
	}

	for _, assembly := range workers {
		for _, metric := range trainingPerformanceMetrics {
			values := assembly.latestValues[metric.name]
			metricSeries := make([]domain.TrainingMetricSeries, 0, len(values))
			for _, value := range values {
				metricSeries = append(metricSeries, domain.TrainingMetricSeries{Points: []domain.TrainingMetricPoint{{Value: value}}})
			}
			assembly.worker.Summary[metric.name] = reduceTrainingMetric(metric.reducer, metricSeries)
		}
		if value := assembly.worker.Summary["restarts"]; value != nil && *value >= 0 {
			restarts := int(*value)
			assembly.worker.Restarts = &restarts
		}
		if value := assembly.worker.Summary["step"]; value != nil && *value >= 0 {
			step := int64(*value)
			assembly.worker.Step = &step
		}
	}
	return workers
}

func preferredTrainingPod(labels map[string]string) string {
	return firstLabel(labels, "exported_pod", "pod")
}

func trainingWorkerAliases(pod string, labels map[string]string) []string {
	if pod == "" {
		return nil
	}
	result := make([]string, 0, 3)
	if gpu := labels["gpu"]; gpu != "" {
		result = append(result, pod+"|identity:"+gpu)
	}
	if uuid := labels["UUID"]; uuid != "" {
		result = append(result, pod+"|identity:"+uuid)
	}
	if rank := firstLabel(labels, "rank", "worker_rank"); rank != "" {
		result = append(result, pod+"|rank:"+rank)
	}
	return result
}

func findTrainingWorker(index map[string]*trainingWorkerAssembly, aliases []string) *trainingWorkerAssembly {
	for _, alias := range aliases {
		if worker := index[alias]; worker != nil {
			return worker
		}
	}
	return nil
}

func registerTrainingWorkerAliases(index map[string]*trainingWorkerAssembly, worker *trainingWorkerAssembly, aliases []string) {
	for _, alias := range aliases {
		if index[alias] == nil {
			index[alias] = worker
		}
	}
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
