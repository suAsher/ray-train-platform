package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// GPUDevice is one physical accelerator as DCGM Exporter reports it. The
// Portal previously showed only Kubernetes GPU *requests*, which says how much
// capacity is reserved but nothing about whether it is being used.
type GPUDevice struct {
	UUID               string  `json:"uuid"`
	NodeName           string  `json:"nodeName"`
	Index              string  `json:"index"`
	Model              string  `json:"model"`
	UtilizationPercent float64 `json:"utilizationPercent"`
	MemoryUsedMiB      float64 `json:"memoryUsedMib"`
	MemoryTotalMiB     float64 `json:"memoryTotalMib"`
	TemperatureCelsius float64 `json:"temperatureCelsius"`
	PowerWatts         float64 `json:"powerWatts"`
	Busy               bool    `json:"busy"`
}

type GPUInventory struct {
	Devices   []GPUDevice `json:"devices"`
	TotalGPUs int         `json:"totalGpus"`
	BusyGPUs  int         `json:"busyGpus"`
}

// gpuBusyUtilizationPercent is the point above which a card counts as working.
// DCGM reports a few percent of noise on an idle card, and treating that as
// busy makes an idle fleet look saturated.
const gpuBusyUtilizationPercent = 5

var gpuMetricNames = struct {
	utilization, memoryUsed, memoryFree, temperature, power string
}{
	utilization: "DCGM_FI_DEV_GPU_UTIL",
	memoryUsed:  "DCGM_FI_DEV_FB_USED",
	memoryFree:  "DCGM_FI_DEV_FB_FREE",
	temperature: "DCGM_FI_DEV_GPU_TEMP",
	power:       "DCGM_FI_DEV_POWER_USAGE",
}

// QueryGPUInventory reads live per-GPU state from DCGM Exporter through
// Prometheus. Utilization carries the identity labels; the remaining series
// are joined on the GPU UUID.
func (c *PrometheusClient) QueryGPUInventory(ctx context.Context) (GPUInventory, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return GPUInventory{}, fmt.Errorf("Prometheus URL is not configured")
	}
	utilization, err := c.instantQuery(ctx, gpuMetricNames.utilization)
	if err != nil {
		return GPUInventory{}, err
	}
	memoryUsed, err := c.instantQuery(ctx, gpuMetricNames.memoryUsed)
	if err != nil {
		return GPUInventory{}, err
	}
	memoryFree, err := c.instantQuery(ctx, gpuMetricNames.memoryFree)
	if err != nil {
		return GPUInventory{}, err
	}
	temperature, err := c.instantQuery(ctx, gpuMetricNames.temperature)
	if err != nil {
		return GPUInventory{}, err
	}
	power, err := c.instantQuery(ctx, gpuMetricNames.power)
	if err != nil {
		return GPUInventory{}, err
	}

	byUUID := func(samples []promSample) map[string]float64 {
		values := make(map[string]float64, len(samples))
		for _, sample := range samples {
			values[sample.labels["UUID"]] = sample.value
		}
		return values
	}
	used, free := byUUID(memoryUsed), byUUID(memoryFree)
	temperatures, watts := byUUID(temperature), byUUID(power)

	inventory := GPUInventory{Devices: make([]GPUDevice, 0, len(utilization))}
	for _, sample := range utilization {
		uuid := sample.labels["UUID"]
		device := GPUDevice{
			UUID:               uuid,
			NodeName:           sample.labels["Hostname"],
			Index:              sample.labels["gpu"],
			Model:              sample.labels["modelName"],
			UtilizationPercent: sample.value,
			MemoryUsedMiB:      used[uuid],
			MemoryTotalMiB:     used[uuid] + free[uuid],
			TemperatureCelsius: temperatures[uuid],
			PowerWatts:         watts[uuid],
			Busy:               sample.value > gpuBusyUtilizationPercent,
		}
		inventory.Devices = append(inventory.Devices, device)
		if device.Busy {
			inventory.BusyGPUs++
		}
	}
	// Group by node, then by index, so a page can render one machine at a time.
	sort.SliceStable(inventory.Devices, func(left, right int) bool {
		if inventory.Devices[left].NodeName != inventory.Devices[right].NodeName {
			return inventory.Devices[left].NodeName < inventory.Devices[right].NodeName
		}
		return inventory.Devices[left].Index < inventory.Devices[right].Index
	})
	inventory.TotalGPUs = len(inventory.Devices)
	return inventory, nil
}

type promSample struct {
	labels map[string]string
	value  float64
}

// maxPrometheusResponseBytes bounds an unexpectedly large series set so a
// misconfigured Prometheus cannot exhaust the control plane's memory.
const maxPrometheusResponseBytes = 8 * 1024 * 1024

func (c *PrometheusClient) instantQuery(ctx context.Context, expression string) ([]promSample, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/api/v1/query")
	if err != nil {
		return nil, fmt.Errorf("parse Prometheus URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("query", expression)
	endpoint.RawQuery = query.Encode()

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Prometheus request: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query Prometheus: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Prometheus returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPrometheusResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read Prometheus response: %w", err)
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("Prometheus query was not successful")
	}
	samples := make([]promSample, 0, len(payload.Data.Result))
	for _, result := range payload.Data.Result {
		if len(result.Value) != 2 {
			continue
		}
		text, ok := result.Value[1].(string)
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			continue
		}
		samples = append(samples, promSample{labels: result.Metric, value: value})
	}
	return samples, nil
}
