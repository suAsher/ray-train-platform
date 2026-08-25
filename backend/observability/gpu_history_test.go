package observability

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestJobGPUHistoryScopesEveryMetricQueryToPersistedWorkloadLabels(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		expression := request.URL.Query().Get("query")
		expectedSelector := `{exported_namespace="tenant-local",exported_pod=~"train\\.cluster-worker-.*"}`
		if !strings.Contains(expression, expectedSelector) {
			t.Fatalf("job GPU history query was not workload-scoped: %s", expression)
		}
		if !strings.Contains(expression, "avg_over_time(") || !strings.Contains(expression, "[1m])") {
			t.Fatalf("job GPU history query was not smoothed over one minute: %s", expression)
		}
		if !strings.HasPrefix(expression, "avg by (UUID, Hostname, gpu, modelName) (") {
			t.Fatalf("job GPU history query must aggregate by physical GPU: %s", expression)
		}
		body := `{"status":"success","data":{"result":[]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}

	history, err := client.QueryJobGPUHistory(context.Background(), "1h", "tenant-local", "train.cluster")
	if err != nil {
		t.Fatalf("query job GPU history: %v", err)
	}
	if requests != len(gpuHistoryMetrics) {
		t.Fatalf("expected one scoped request per GPU metric, got %d", requests)
	}
	if history.Window != "1h" || history.StepSeconds != 30 || len(history.Devices) != 0 {
		t.Fatalf("unexpected history envelope: %+v", history)
	}
}

func TestJobGPUHistoryEmptyClusterReturnsBoundedEmptyHistoryWithoutRequest(t *testing.T) {
	client := PrometheusClient{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("empty RayCluster must not query Prometheus: %s", request.URL)
		return nil, nil
	})}}

	history, err := client.QueryJobGPUHistory(context.Background(), "6h", "tenant-local", "")
	if err != nil {
		t.Fatalf("query empty job GPU history: %v", err)
	}
	if history.Window != "6h" || history.StepSeconds != 60 || !history.EndedAt.After(history.StartedAt) {
		t.Fatalf("unexpected bounded history envelope: %+v", history)
	}
	if history.EndedAt.Sub(history.StartedAt) != 6*time.Hour {
		t.Fatalf("history duration = %s, want 6h", history.EndedAt.Sub(history.StartedAt))
	}
	if history.Devices == nil || len(history.Devices) != 0 {
		t.Fatalf("empty history devices must be a non-nil empty slice: %#v", history.Devices)
	}
}

func TestJobGPUHistoryRejectsUnsafeMetadataWithoutRequest(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unsafe metadata must not query Prometheus: %s", request.URL)
		return nil, nil
	})}}
	tests := []struct {
		name           string
		namespace      string
		rayClusterName string
	}{
		{name: "namespace injection", namespace: `tenant"}`, rayClusterName: "train-cluster"},
		{name: "cluster regex injection", namespace: "tenant-local", rayClusterName: `train.*`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.QueryJobGPUHistory(context.Background(), "1h", test.namespace, test.rayClusterName); err == nil {
				t.Fatal("unsafe workload metadata must be rejected")
			}
		})
	}
}

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
