package observability

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestGPUHistoryUsesBoundedWindowAndJoinsMetricsByUUID(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Path != "/api/v1/query_range" || request.URL.Query().Get("step") != "30" {
			t.Fatalf("unexpected range request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		expression := request.URL.Query().Get("query")
		if !strings.Contains(expression, `Hostname="172.28.1.233"`) || !strings.Contains(expression, "avg_over_time") {
			t.Fatalf("history query was not node-scoped and smoothed: %s", expression)
		}
		if !strings.HasPrefix(expression, "avg by (UUID, Hostname, gpu, modelName) (") {
			t.Fatalf("history query must merge changing workload labels by physical GPU: %s", expression)
		}
		value := "1"
		switch {
		case strings.Contains(expression, "GPU_UTIL"):
			value = "92"
		case strings.Contains(expression, "FB_USED"):
			value = "12288"
		case strings.Contains(expression, "GPU_TEMP"):
			value = "54"
		case strings.Contains(expression, "POWER_USAGE"):
			value = "188"
		}
		body := `{"status":"success","data":{"result":[{"metric":{"UUID":"GPU-1","Hostname":"172.28.1.233","gpu":"0","modelName":"RTX 4090 D","exported_namespace":"tenant-local","exported_pod":"job-1-worker"},"values":[[1000,"` + value + `"],[1030,"` + value + `"]]}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}

	history, err := client.QueryGPUHistory(context.Background(), "1h", "172.28.1.233")
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if requests != 4 || history.Window != "1h" || history.StepSeconds != 30 || len(history.Devices) != 1 {
		t.Fatalf("unexpected history envelope: requests=%d history=%+v", requests, history)
	}
	device := history.Devices[0]
	if device.UUID != "GPU-1" || device.NodeName != "172.28.1.233" || device.PodName != "" {
		t.Fatalf("device identity was not joined: %+v", device)
	}
	if got := device.Series.UtilizationPercent[1].Value; got != 92 {
		t.Fatalf("unexpected utilization point: %v", got)
	}
	if got := device.Series.MemoryUsedMiB[0].Value; got != 12288 {
		t.Fatalf("unexpected memory point: %v", got)
	}
}

func TestGPUHistoryAllowsOnlyDocumentedWindows(t *testing.T) {
	windows := map[string]int{"15m": 30, "1h": 30, "6h": 60, "24h": 300, "7d": 900}
	for window, expectedStep := range windows {
		t.Run(window, func(t *testing.T) {
			client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Query().Get("step") != strconv.Itoa(expectedStep) {
					t.Fatalf("window %s used step %s", window, request.URL.Query().Get("step"))
				}
				body := `{"status":"success","data":{"result":[]}}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}}
			if _, err := client.QueryGPUHistory(context.Background(), window, ""); err != nil {
				t.Fatalf("allowed window %s failed: %v", window, err)
			}
		})
	}

	client := PrometheusClient{BaseURL: "http://prometheus"}
	if _, err := client.QueryGPUHistory(context.Background(), "30d", ""); err == nil {
		t.Fatal("unsupported history window must be rejected")
	}
	if _, err := client.QueryGPUHistory(context.Background(), "1h", `node"}`); err == nil {
		t.Fatal("unsafe node label must be rejected")
	}
}
