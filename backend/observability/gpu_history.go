package observability

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type GPUHistorySeries struct {
	UtilizationPercent []MetricPoint `json:"utilizationPercent"`
	MemoryUsedMiB      []MetricPoint `json:"memoryUsedMib"`
	TemperatureCelsius []MetricPoint `json:"temperatureCelsius"`
	PowerWatts         []MetricPoint `json:"powerWatts"`
}

type GPUHistoryDevice struct {
	UUID          string           `json:"uuid"`
	NodeName      string           `json:"nodeName"`
	Index         string           `json:"index"`
	Model         string           `json:"model"`
	Namespace     string           `json:"namespace,omitempty"`
	PodName       string           `json:"podName,omitempty"`
	ContainerName string           `json:"containerName,omitempty"`
	Series        GPUHistorySeries `json:"series"`
}

type GPUHistory struct {
	Window      string             `json:"window"`
	StepSeconds int                `json:"stepSeconds"`
	StartedAt   time.Time          `json:"startedAt"`
	EndedAt     time.Time          `json:"endedAt"`
	Devices     []GPUHistoryDevice `json:"devices"`
}

type gpuHistoryWindow struct {
	duration time.Duration
	step     time.Duration
}

var gpuHistoryWindows = map[string]gpuHistoryWindow{
	"15m": {duration: 15 * time.Minute, step: 30 * time.Second},
	"1h":  {duration: time.Hour, step: 30 * time.Second},
	"6h":  {duration: 6 * time.Hour, step: time.Minute},
	"24h": {duration: 24 * time.Hour, step: 5 * time.Minute},
	"7d":  {duration: 7 * 24 * time.Hour, step: 15 * time.Minute},
}

type gpuHistoryMetric struct {
	name   string
	assign func(*GPUHistorySeries, []MetricPoint)
}

var gpuHistoryMetrics = []gpuHistoryMetric{
	{name: gpuMetricNames.utilization, assign: func(series *GPUHistorySeries, points []MetricPoint) { series.UtilizationPercent = points }},
	{name: gpuMetricNames.memoryUsed, assign: func(series *GPUHistorySeries, points []MetricPoint) { series.MemoryUsedMiB = points }},
	{name: gpuMetricNames.temperature, assign: func(series *GPUHistorySeries, points []MetricPoint) { series.TemperatureCelsius = points }},
	{name: gpuMetricNames.power, assign: func(series *GPUHistorySeries, points []MetricPoint) { series.PowerWatts = points }},
}

func (c *PrometheusClient) QueryGPUHistory(ctx context.Context, window, nodeName string) (GPUHistory, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return GPUHistory{}, fmt.Errorf("Prometheus URL is not configured")
	}
	history, spec, err := newGPUHistory(window)
	if err != nil {
		return GPUHistory{}, err
	}
	selector := ""
	if nodeName != "" {
		if !safeGPUNodeLabel(nodeName) {
			return GPUHistory{}, fmt.Errorf("GPU node name is invalid")
		}
		selector = fmt.Sprintf(`{Hostname="%s"}`, nodeName)
	}
	return c.queryGPUHistory(ctx, history, spec, selector)
}

func (c *PrometheusClient) QueryJobGPUHistory(ctx context.Context, window, namespace, rayClusterName string) (GPUHistory, error) {
	history, spec, err := newGPUHistory(window)
	if err != nil {
		return GPUHistory{}, err
	}
	if rayClusterName == "" {
		return history, nil
	}
	if len(validation.IsDNS1123Label(namespace)) != 0 {
		return GPUHistory{}, fmt.Errorf("GPU workload namespace is invalid")
	}
	if len(validation.IsDNS1123Subdomain(rayClusterName)) != 0 {
		return GPUHistory{}, fmt.Errorf("GPU workload RayCluster name is invalid")
	}
	selector := fmt.Sprintf(
		`{exported_namespace="%s",exported_pod=~"%s-worker-.*"}`,
		namespace,
		strings.ReplaceAll(regexp.QuoteMeta(rayClusterName), `\`, `\\`),
	)
	return c.queryGPUHistory(ctx, history, spec, selector)
}

func newGPUHistory(window string) (GPUHistory, gpuHistoryWindow, error) {
	spec, ok := gpuHistoryWindows[window]
	if !ok {
		return GPUHistory{}, gpuHistoryWindow{}, fmt.Errorf("unsupported GPU history window")
	}
	end := time.Now().UTC()
	start := end.Add(-spec.duration)
	history := GPUHistory{
		Window: window, StepSeconds: int(spec.step / time.Second),
		StartedAt: start, EndedAt: end, Devices: []GPUHistoryDevice{},
	}
	return history, spec, nil
}

func (c *PrometheusClient) queryGPUHistory(ctx context.Context, history GPUHistory, spec gpuHistoryWindow, selector string) (GPUHistory, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return GPUHistory{}, fmt.Errorf("Prometheus URL is not configured")
	}
	devices := map[string]*GPUHistoryDevice{}
	for _, metric := range gpuHistoryMetrics {
		expression := metric.name + selector
		// Workload labels (pod/container/namespace) change when a GPU moves
		// between jobs. Collapse them so one physical GPU remains one continuous
		// line instead of an arbitrary set of short-lived pod-labelled series.
		expression = fmt.Sprintf("avg by (UUID, Hostname, gpu, modelName) (avg_over_time(%s[1m]))", expression)
		series, err := c.queryRangeWithStep(ctx, expression, history.StartedAt, history.EndedAt, spec.step)
		if err != nil {
			return GPUHistory{}, err
		}
		for _, item := range series {
			uuid := item.Labels["UUID"]
			if uuid == "" {
				continue
			}
			device := devices[uuid]
			if device == nil {
				device = &GPUHistoryDevice{
					UUID: uuid, NodeName: item.Labels["Hostname"], Index: item.Labels["gpu"], Model: item.Labels["modelName"],
				}
				devices[uuid] = device
			}
			metric.assign(&device.Series, append([]MetricPoint(nil), item.Points...))
		}
	}
	for _, device := range devices {
		history.Devices = append(history.Devices, *device)
	}
	sort.SliceStable(history.Devices, func(left, right int) bool {
		if history.Devices[left].NodeName != history.Devices[right].NodeName {
			return history.Devices[left].NodeName < history.Devices[right].NodeName
		}
		return history.Devices[left].Index < history.Devices[right].Index
	})
	return history, nil
}

func safeGPUNodeLabel(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}
