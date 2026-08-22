package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// dcgmVector renders a Prometheus instant-query response shaped like the one
// DCGM Exporter produces, so the parser is tested against the real label set.
func dcgmVector(samples ...map[string]any) string {
	results := make([]map[string]any, 0, len(samples))
	for _, sample := range samples {
		value := sample["value"]
		delete(sample, "value")
		results = append(results, map[string]any{"metric": sample, "value": []any{1787126720.863, value}})
	}
	payload, _ := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"resultType": "vector", "result": results},
	})
	return string(payload)
}

func gpuMetricsServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		body, ok := responses[query]
		if !ok {
			t.Errorf("unexpected query %q", query)
			body = dcgmVector()
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	}))
}

// The portal showed only Kubernetes GPU *requests* and told operators that
// utilization "requires deploying DCGM Exporter" — which was already deployed.
// These are the live per-GPU readings that were being discarded.
func TestQueryGPUInventoryReportsPerDeviceUtilisationAndMemory(t *testing.T) {
	server := gpuMetricsServer(t, map[string]string{
		"DCGM_FI_DEV_GPU_UTIL": dcgmVector(
			map[string]any{"Hostname": "172.28.1.232", "gpu": "0", "UUID": "GPU-aaa", "modelName": "NVIDIA GeForce RTX 4090 D", "value": "73"},
			map[string]any{"Hostname": "172.28.1.233", "gpu": "1", "UUID": "GPU-bbb", "modelName": "NVIDIA GeForce RTX 4090 D", "value": "0"},
		),
		"DCGM_FI_DEV_FB_USED":     dcgmVector(map[string]any{"UUID": "GPU-aaa", "value": "18000"}, map[string]any{"UUID": "GPU-bbb", "value": "2"}),
		"DCGM_FI_DEV_FB_FREE":     dcgmVector(map[string]any{"UUID": "GPU-aaa", "value": "6000"}, map[string]any{"UUID": "GPU-bbb", "value": "24000"}),
		"DCGM_FI_DEV_GPU_TEMP":    dcgmVector(map[string]any{"UUID": "GPU-aaa", "value": "71"}, map[string]any{"UUID": "GPU-bbb", "value": "34"}),
		"DCGM_FI_DEV_POWER_USAGE": dcgmVector(map[string]any{"UUID": "GPU-aaa", "value": "310.5"}, map[string]any{"UUID": "GPU-bbb", "value": "45.2"}),
	})
	defer server.Close()

	client := &PrometheusClient{BaseURL: server.URL}
	inventory, err := client.QueryGPUInventory(context.Background())
	if err != nil {
		t.Fatalf("query gpu inventory: %v", err)
	}
	if len(inventory.Devices) != 2 {
		t.Fatalf("expected two devices, got %d", len(inventory.Devices))
	}
	busy := inventory.Devices[0]
	if busy.UUID != "GPU-aaa" || busy.NodeName != "172.28.1.232" || busy.Index != "0" {
		t.Fatalf("unexpected device identity: %+v", busy)
	}
	if busy.UtilizationPercent != 73 || busy.TemperatureCelsius != 71 || busy.PowerWatts != 310.5 {
		t.Fatalf("unexpected readings: %+v", busy)
	}
	// DCGM reports framebuffer in MiB; total is used + free.
	if busy.MemoryUsedMiB != 18000 || busy.MemoryTotalMiB != 24000 {
		t.Fatalf("unexpected memory: %+v", busy)
	}
	if inventory.TotalGPUs != 2 || inventory.BusyGPUs != 1 {
		t.Fatalf("expected 2 GPUs with 1 busy, got %+v", inventory)
	}
}

// Devices are grouped by node so the page can show a machine at a time.
func TestQueryGPUInventoryOrdersDevicesByNodeThenIndex(t *testing.T) {
	server := gpuMetricsServer(t, map[string]string{
		"DCGM_FI_DEV_GPU_UTIL": dcgmVector(
			map[string]any{"Hostname": "node-b", "gpu": "1", "UUID": "GPU-3", "value": "10"},
			map[string]any{"Hostname": "node-a", "gpu": "1", "UUID": "GPU-2", "value": "10"},
			map[string]any{"Hostname": "node-a", "gpu": "0", "UUID": "GPU-1", "value": "10"},
		),
		"DCGM_FI_DEV_FB_USED":     dcgmVector(),
		"DCGM_FI_DEV_FB_FREE":     dcgmVector(),
		"DCGM_FI_DEV_GPU_TEMP":    dcgmVector(),
		"DCGM_FI_DEV_POWER_USAGE": dcgmVector(),
	})
	defer server.Close()

	inventory, err := (&PrometheusClient{BaseURL: server.URL}).QueryGPUInventory(context.Background())
	if err != nil {
		t.Fatalf("query gpu inventory: %v", err)
	}
	got := make([]string, 0, len(inventory.Devices))
	for _, device := range inventory.Devices {
		got = append(got, device.NodeName+"/"+device.Index)
	}
	want := []string{"node-a/0", "node-a/1", "node-b/1"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// An unconfigured or unreachable Prometheus must be reported, not rendered as
// a cluster with zero GPUs.
func TestQueryGPUInventoryReportsAnUnconfiguredPrometheus(t *testing.T) {
	if _, err := (&PrometheusClient{}).QueryGPUInventory(context.Background()); err == nil {
		t.Fatal("expected an error when Prometheus is not configured")
	}
}

// A GPU is "busy" only above a threshold: DCGM reports small non-zero noise on
// an idle card, and counting that as busy makes an idle fleet look saturated.
func TestGPUBusyThresholdIgnoresIdleNoise(t *testing.T) {
	server := gpuMetricsServer(t, map[string]string{
		"DCGM_FI_DEV_GPU_UTIL": dcgmVector(
			map[string]any{"Hostname": "node-a", "gpu": "0", "UUID": "GPU-1", "value": "2"},
			map[string]any{"Hostname": "node-a", "gpu": "1", "UUID": "GPU-2", "value": "0"},
		),
		"DCGM_FI_DEV_FB_USED":     dcgmVector(),
		"DCGM_FI_DEV_FB_FREE":     dcgmVector(),
		"DCGM_FI_DEV_GPU_TEMP":    dcgmVector(),
		"DCGM_FI_DEV_POWER_USAGE": dcgmVector(),
	})
	defer server.Close()

	inventory, _ := (&PrometheusClient{BaseURL: server.URL}).QueryGPUInventory(context.Background())
	if inventory.BusyGPUs != 0 {
		t.Fatalf("idle noise must not count as busy, got %d", inventory.BusyGPUs)
	}
}
