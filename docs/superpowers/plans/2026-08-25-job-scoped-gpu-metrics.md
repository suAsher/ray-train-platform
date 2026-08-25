# Job-Scoped GPU Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an authenticated training user inspect utilization, memory, power, and temperature history for only the GPUs assigned to an authorized training job.

**Architecture:** Add a job-scoped Prometheus query that derives namespace and RayCluster worker selectors exclusively from the persisted job record. Expose it through the existing job authorization surface, then reuse the current immutable GPU history model and ECharts component in `JobDetail.vue` with an independent 30-second refresh loop that preserves the last successful sample.

**Tech Stack:** Go 1.25, Gin, Prometheus HTTP API, Vue 3, Element Plus, ECharts, Node test runner, Go testing.

---

## File map

- Modify `backend/observability/gpu_history.go`: factor the shared range-query loop and add a workload-scoped selector.
- Modify `backend/observability/gpu_history_test.go`: verify namespace and RayCluster worker filtering, bounded windows, empty-cluster behavior, and injection rejection.
- Modify `backend/api/jobs.go`: add the provider contract and authorized job GPU history handler.
- Modify `backend/api/jobs_scoped_routes.go`: register the read-scoped endpoint.
- Create `backend/api/jobs_gpu_metrics_test.go`: verify owner/team/platform/PAT boundaries and error behavior.
- Create `frontend/src/api/jobGpuMetrics.js`: build the same-origin job history request without accepting PromQL.
- Modify `frontend/src/gpuMetrics.js`: add all-job summary and chart-series helpers without mutating payloads.
- Create `frontend/src/jobGpuMetrics.test.js`: test request encoding, normalization, summary, and per-device series labels.
- Create `frontend/src/jobGpuMetricsUi.test.js`: enforce the task-detail UI contract and refresh/failure behavior.
- Modify `frontend/src/views/Job/JobDetail.vue`: render task-scoped summary and four trend charts.
- Modify `docs/USER_GUIDE.md`: explain what users see and why a completed task can eventually have no retained samples.
- Modify `docs/OPERATIONS_GUIDE.md`: document the selector and non-disruptive troubleshooting path.

### Task 1: Workload-scoped Prometheus query

**Files:**
- Modify: `backend/observability/gpu_history.go`
- Modify: `backend/observability/gpu_history_test.go`

- [ ] **Step 1: Write failing workload-scope tests**

Append tests that assert every DCGM query contains both trusted labels and that an absent RayCluster causes zero Prometheus requests:

```go
func TestJobGPUHistoryScopesEveryMetricToNamespaceAndWorkerPods(t *testing.T) {
	requests := 0
	client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		expression := request.URL.Query().Get("query")
		if !strings.Contains(expression, `exported_namespace="tenant-team-a"`) {
			t.Fatalf("query escaped the job namespace: %s", expression)
		}
		if !strings.Contains(expression, `exported_pod=~"train-cluster-worker-.*"`) {
			t.Fatalf("query escaped the Ray worker prefix: %s", expression)
		}
		body := `{"status":"success","data":{"result":[{"metric":{"UUID":"GPU-1","Hostname":"node-a","gpu":"0","modelName":"RTX 4090 D"},"values":[[1000,"75"]]}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}

	history, err := client.QueryJobGPUHistory(context.Background(), "1h", "tenant-team-a", "train-cluster")
	if err != nil {
		t.Fatalf("query job GPU history: %v", err)
	}
	if requests != 4 || len(history.Devices) != 1 || history.Devices[0].UUID != "GPU-1" {
		t.Fatalf("unexpected scoped history: requests=%d history=%+v", requests, history)
	}
}

func TestJobGPUHistoryDoesNotQueryWithoutObservedRayCluster(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("empty RayCluster must not query Prometheus: %s", request.URL)
		return nil, nil
	})}}

	history, err := client.QueryJobGPUHistory(context.Background(), "15m", "tenant-team-a", "")
	if err != nil {
		t.Fatalf("empty RayCluster history: %v", err)
	}
	if history.Window != "15m" || len(history.Devices) != 0 {
		t.Fatalf("unexpected empty history: %+v", history)
	}
}

func TestJobGPUHistoryRejectsUnsafePersistedMetadata(t *testing.T) {
	client := PrometheusClient{BaseURL: "http://prometheus"}
	for _, input := range [][2]string{{`tenant-a"}`, "cluster-a"}, {"tenant-a", `cluster-a|.*`}} {
		if _, err := client.QueryJobGPUHistory(context.Background(), "1h", input[0], input[1]); err == nil {
			t.Fatalf("unsafe metadata was accepted: namespace=%q cluster=%q", input[0], input[1])
		}
	}
}
```

- [ ] **Step 2: Run the focused tests and observe RED**

Run:

```bash
go test ./observability -run 'TestJobGPUHistory' -count=1
```

Expected: compilation fails because `QueryJobGPUHistory` does not exist.

- [ ] **Step 3: Implement the trusted workload selector and shared query loop**

Add these functions to `backend/observability/gpu_history.go`, and make existing `QueryGPUHistory` call `queryGPUHistory` after building its optional node selector:

```go
func (c *PrometheusClient) QueryJobGPUHistory(ctx context.Context, window, namespace, rayClusterName string) (GPUHistory, error) {
	if strings.TrimSpace(rayClusterName) == "" {
		history, _, err := newGPUHistory(window)
		return history, err
	}
	if !safeGPUWorkloadLabel(namespace) || !safeGPUWorkloadLabel(rayClusterName) {
		return GPUHistory{}, fmt.Errorf("GPU workload identity is invalid")
	}
	selector := fmt.Sprintf(`exported_namespace="%s",exported_pod=~"%s-worker-.*"`, namespace, regexp.QuoteMeta(rayClusterName))
	return c.queryGPUHistory(ctx, window, selector)
}

func (c *PrometheusClient) queryGPUHistory(ctx context.Context, window, selector string) (GPUHistory, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return GPUHistory{}, fmt.Errorf("Prometheus URL is not configured")
	}
	history, spec, err := newGPUHistory(window)
	if err != nil {
		return GPUHistory{}, err
	}
	devices := map[string]*GPUHistoryDevice{}
	for _, metric := range gpuHistoryMetrics {
		metricSelector := metric.name
		if selector != "" {
			metricSelector += "{" + selector + "}"
		}
		expression := fmt.Sprintf("avg by (UUID, Hostname, gpu, modelName) (avg_over_time(%s[1m]))", metricSelector)
		series, err := c.queryRangeWithStep(ctx, expression, history.StartedAt, history.EndedAt, spec.step)
		if err != nil {
			return GPUHistory{}, err
		}
		joinGPUHistorySeries(devices, metric, series)
	}
	history.Devices = sortedGPUHistoryDevices(devices)
	return history, nil
}

func newGPUHistory(window string) (GPUHistory, gpuHistoryWindow, error) {
	spec, ok := gpuHistoryWindows[window]
	if !ok {
		return GPUHistory{}, gpuHistoryWindow{}, fmt.Errorf("unsupported GPU history window")
	}
	end := time.Now().UTC()
	return GPUHistory{
		Window: window, StepSeconds: int(spec.step / time.Second),
		StartedAt: end.Add(-spec.duration), EndedAt: end, Devices: []GPUHistoryDevice{},
	}, spec, nil
}

func safeGPUWorkloadLabel(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
```

Import `regexp`. Extract the current device-join and sort blocks into these helpers, preserving the existing global query result exactly:

```go
func joinGPUHistorySeries(devices map[string]*GPUHistoryDevice, metric gpuHistoryMetric, series []MetricSeries) {
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

func sortedGPUHistoryDevices(devices map[string]*GPUHistoryDevice) []GPUHistoryDevice {
	result := make([]GPUHistoryDevice, 0, len(devices))
	for _, device := range devices {
		result = append(result, *device)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].NodeName != result[right].NodeName {
			return result[left].NodeName < result[right].NodeName
		}
		return result[left].Index < result[right].Index
	})
	return result
}
```

- [ ] **Step 4: Run observability tests and observe GREEN**

Run:

```bash
go test ./observability -count=1
```

Expected: all `observability` tests pass, including existing global GPU history tests.

- [ ] **Step 5: Commit the query layer**

```bash
git add backend/observability/gpu_history.go backend/observability/gpu_history_test.go
git commit -m "feat: query GPU history by training workload"
```

### Task 2: Authorized job GPU metrics API

**Files:**
- Modify: `backend/api/jobs.go`
- Modify: `backend/api/jobs_scoped_routes.go`
- Create: `backend/api/jobs_gpu_metrics_test.go`

- [ ] **Step 1: Write failing API authorization tests**

Create a fake provider that records only server-derived metadata, then cover owner, same-team engineer, team administrator, platform administrator, PAT scope, invalid window, and upstream failure:

```go
type fakeJobGPUHistoryProvider struct {
	window, namespace, cluster string
	calls int
	err error
}

func (provider *fakeJobGPUHistoryProvider) QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error) {
	return observability.JobMetrics{}, nil
}

func (provider *fakeJobGPUHistoryProvider) QueryJobGPUHistory(_ context.Context, window, namespace, cluster string) (observability.GPUHistory, error) {
	provider.calls++
	provider.window, provider.namespace, provider.cluster = window, namespace, cluster
	return observability.GPUHistory{Window: window, Devices: []observability.GPUHistoryDevice{}}, provider.err
}
```

Use a repository containing:

```go
job := domain.TrainingJob{
	ID: "job-a", TenantID: "team-a", UserID: "owner-a",
	KubernetesNS: "tenant-team-a", RayClusterName: "train-cluster-a",
}
```

Assert the following response matrix:

```go
tests := []struct {
	name string
	principal auth.Principal
	want int
}{
	{"owner", auth.Principal{Subject: "owner-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}, http.StatusOK},
	{"same team engineer", auth.Principal{Subject: "engineer-b", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}, http.StatusNotFound},
	{"team administrator", auth.Principal{Subject: "lead-a", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal}, http.StatusOK},
	{"other team administrator", auth.Principal{Subject: "lead-b", TenantID: "team-b", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal}, http.StatusNotFound},
	{"platform administrator", auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}, http.StatusOK},
	{"owner PAT", auth.Principal{Subject: "owner-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsRead}}, http.StatusOK},
}
```

For successful cases assert `window == "1h"`, `namespace == "tenant-team-a"`, and `cluster == "train-cluster-a"`. Add separate tests proving `window=30d` returns 400 before the provider and a provider error returns 502.

- [ ] **Step 2: Run API tests and observe RED**

Run:

```bash
go test ./api -run 'TestJobGPU' -count=1
```

Expected: requests return 404 because the route and handler do not exist.

- [ ] **Step 3: Register the read-scoped endpoint**

Add to `registerTrainingRoutes`:

```go
read.GET("/jobs/:id/gpu-metrics", h.getJobGPUHistory)
```

This keeps `jobs:read` PAT enforcement identical to job details, logs, metrics, and artifacts.

- [ ] **Step 4: Implement the handler with owner/team/platform boundaries**

Add the provider contract and handler to `backend/api/jobs.go`:

```go
type JobGPUHistoryProvider interface {
	QueryJobGPUHistory(context.Context, string, string, string) (observability.GPUHistory, error)
}

func (h *Handler) getJobGPUHistory(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	provider, ok := h.metrics.(JobGPUHistoryProvider)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "GPU_METRICS_UNAVAILABLE", "GPU metrics service is not configured")
		return
	}
	window := strings.TrimSpace(c.DefaultQuery("window", "1h"))
	if _, ok := allowedGPUHistoryWindows[window]; !ok {
		h.writeError(c, http.StatusBadRequest, "INVALID_GPU_METRICS_WINDOW", "GPU metrics window is not supported")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil || (!principal.Allowed(domain.RoleTenantAdmin) && job.UserID != principal.Subject) {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	history, err := provider.QueryJobGPUHistory(c.Request.Context(), window, job.KubernetesNS, job.RayClusterName)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "GPU_METRICS_QUERY_FAILED", "could not query job GPU metrics")
		return
	}
	h.writeSuccess(c, http.StatusOK, history)
}
```

- [ ] **Step 5: Run API and scope regression tests**

Run:

```bash
go test ./api -run 'TestJobGPU|TestTrainingRoutesApplyReadAndWritePATScopes|TestSuperAdmin' -count=1
```

Expected: all selected tests pass; a write-only PAT remains forbidden.

- [ ] **Step 6: Commit the API layer**

```bash
git add backend/api/jobs.go backend/api/jobs_scoped_routes.go backend/api/jobs_gpu_metrics_test.go
git commit -m "feat: expose authorized job GPU metrics"
```

### Task 3: Frontend API and immutable job GPU model

**Files:**
- Create: `frontend/src/api/jobGpuMetrics.js`
- Modify: `frontend/src/gpuMetrics.js`
- Create: `frontend/src/jobGpuMetrics.test.js`

- [ ] **Step 1: Write failing frontend model tests**

Create `frontend/src/jobGpuMetrics.test.js`:

```js
import test from 'node:test'
import assert from 'node:assert/strict'
import { jobGPUHistoryPath } from './api/jobGpuMetrics.js'
import { jobMetricChartSeries, jobMetricSummary, normalizeGPUHistory } from './gpuMetrics.js'

const payload = {
  window: '1h', endedAt: '2026-08-25T08:00:00Z', devices: [
    { uuid: 'gpu-a', nodeName: 'node-a', index: '0', series: {
      utilizationPercent: [{ timestamp: '2026-08-25T08:00:00Z', value: 90 }],
      memoryUsedMib: [{ timestamp: '2026-08-25T08:00:00Z', value: 12000 }],
      powerWatts: [{ timestamp: '2026-08-25T08:00:00Z', value: 220 }],
      temperatureCelsius: [{ timestamp: '2026-08-25T08:00:00Z', value: 60 }],
    } },
    { uuid: 'gpu-b', nodeName: 'node-b', index: '0', series: {
      utilizationPercent: [{ timestamp: '2026-08-25T08:00:00Z', value: 30 }],
      memoryUsedMib: [{ timestamp: '2026-08-25T08:00:00Z', value: 8000 }],
      powerWatts: [{ timestamp: '2026-08-25T08:00:00Z', value: 160 }],
      temperatureCelsius: [{ timestamp: '2026-08-25T08:00:00Z', value: 50 }],
    } },
  ],
}

test('job GPU history path accepts only an encoded job ID and bounded window', () => {
  assert.equal(jobGPUHistoryPath('job/a', '6h'), '/api/v1/jobs/job%2Fa/gpu-metrics?window=6h')
  assert.equal(jobGPUHistoryPath('job-a', 'invalid'), '/api/v1/jobs/job-a/gpu-metrics?window=1h')
})

test('job summary spans every assigned device without mutating the payload', () => {
  const original = structuredClone(payload)
  const summary = jobMetricSummary(normalizeGPUHistory(payload))
  assert.deepEqual(summary, {
    deviceCount: 2, averageUtilizationPercent: 60, totalMemoryUsedMib: 20000,
    totalPowerWatts: 380, maximumTemperatureCelsius: 60, utilizationSpread: 60, imbalanced: true,
  })
  assert.deepEqual(payload, original)
})

test('job chart labels disambiguate GPUs on different nodes', () => {
  const series = jobMetricChartSeries(normalizeGPUHistory(payload), 'utilizationPercent')
  assert.deepEqual(series.map((item) => item.name), ['node-a / GPU 0', 'node-b / GPU 0'])
})
```

- [ ] **Step 2: Run the focused tests and observe RED**

Run:

```bash
npm test -- --test-name-pattern='job GPU|job summary|job chart'
```

Expected: imports fail because the new API and helpers do not exist.

- [ ] **Step 3: Add the bounded request builder**

Create `frontend/src/api/jobGpuMetrics.js`:

```js
import { apiGet } from './client'
import { GPU_TIME_WINDOWS } from '../gpuMetrics'

const allowedWindows = new Set(GPU_TIME_WINDOWS.map((item) => item.value))

export function jobGPUHistoryPath(jobId, window = '1h') {
  const selectedWindow = allowedWindows.has(window) ? window : '1h'
  return `/api/v1/jobs/${encodeURIComponent(String(jobId || ''))}/gpu-metrics?window=${encodeURIComponent(selectedWindow)}`
}

export function fetchJobGPUHistory(jobId, window = '1h') {
  return apiGet(jobGPUHistoryPath(jobId, window))
}
```

- [ ] **Step 4: Add immutable all-job summary and series helpers**

Refactor the current node-only summary through an internal `summarizeDevices` function and export:

```js
export function jobMetricSummary(history) {
  return { deviceCount: history?.devices?.length || 0, ...summarizeDevices(history?.devices || []) }
}

export function jobMetricChartSeries(history, metric) {
  if (!METRICS.includes(metric)) return []
  return [...(history?.devices || [])]
    .sort((left, right) => left.nodeName.localeCompare(right.nodeName) || Number(left.index) - Number(right.index))
    .map((device) => ({
      id: device.uuid,
      name: `${device.nodeName} / GPU ${device.index}`,
      data: device.series[metric].map((point) => [point.timestamp, point.value]),
    }))
}
```

The extracted `summarizeDevices` returns the existing six summary fields, so `nodeMetricSummary` remains backward compatible.

- [ ] **Step 5: Run all frontend model tests**

Run:

```bash
npm test -- --test-name-pattern='GPU|job summary|job chart'
```

Expected: new and existing GPU utility tests pass.

- [ ] **Step 6: Commit the frontend data layer**

```bash
git add frontend/src/api/jobGpuMetrics.js frontend/src/gpuMetrics.js frontend/src/jobGpuMetrics.test.js
git commit -m "feat: model job scoped GPU history"
```

### Task 4: Task-detail GPU summary and trend charts

**Files:**
- Modify: `frontend/src/views/Job/JobDetail.vue`
- Create: `frontend/src/jobGpuMetricsUi.test.js`

- [ ] **Step 1: Write the failing UI contract test**

Create `frontend/src/jobGpuMetricsUi.test.js`:

```js
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const viewPath = new URL('./views/Job/JobDetail.vue', import.meta.url)

test('job detail exposes task-scoped GPU summary and four historical curves', async () => {
  const source = await readFile(viewPath, 'utf8')
  for (const text of ['训练 GPU', '近 1 分钟平均利用率', '显存总使用量', '总功率', '最高温度', '负载不均']) {
    assert.match(source, new RegExp(text))
  }
  for (const title of ['GPU 使用率', '显存使用量', 'GPU 功率', 'GPU 温度']) {
    assert.match(source, new RegExp(title))
  }
  assert.match(source, /fetchJobGPUHistory/)
  assert.match(source, /GPUTrendChart/)
  assert.match(source, /GPU_TIME_WINDOWS/)
  assert.match(source, /setInterval\(refreshJobGPUHistory,\s*30000\)/)
})

test('a failed GPU refresh preserves the last successful history', async () => {
  const source = await readFile(viewPath, 'utf8')
  assert.match(source, /gpuHistory\.value\s*=\s*normalizeGPUHistory\(payload\)/)
  assert.doesNotMatch(source, /catch[\s\S]{0,180}gpuHistory\.value\s*=\s*normalizeGPUHistory\(null\)/)
  assert.match(source, /已加载的 GPU 历史仍可查看/)
})
```

- [ ] **Step 2: Run the focused UI tests and observe RED**

Run:

```bash
npm test -- --test-name-pattern='task-scoped GPU|failed GPU refresh'
```

Expected: assertions fail because the task detail has no GPU history UI.

- [ ] **Step 3: Add state, computed models, and an independent refresh loop**

Add imports:

```js
import GPUTrendChart from '../../components/gpu/GPUTrendChart.vue'
import { fetchJobGPUHistory } from '../../api/jobGpuMetrics'
import { GPU_TIME_WINDOWS, jobMetricChartSeries, jobMetricSummary, normalizeGPUHistory, sampleFreshness } from '../../gpuMetrics'
```

Add state and computed values:

```js
const gpuHistory = ref(normalizeGPUHistory(null))
const selectedGPUWindow = ref('1h')
const gpuMetricsError = ref('')
const gpuSummaryMetrics = computed(() => jobMetricSummary(gpuHistory.value))
const gpuFreshness = computed(() => sampleFreshness(gpuHistory.value.endedAt, nowTick.value))
const gpuChartSeries = metric => jobMetricChartSeries(gpuHistory.value, metric)
const gpuStatCards = computed(() => [
  { label: '参与训练 GPU', value: `${gpuSummaryMetrics.value.deviceCount} 卡` },
  { label: '近 1 分钟平均利用率', value: `${gpuSummaryMetrics.value.averageUtilizationPercent}%` },
  { label: '显存总使用量', value: `${(gpuSummaryMetrics.value.totalMemoryUsedMib / 1024).toFixed(1)} GiB` },
  { label: '总功率', value: `${gpuSummaryMetrics.value.totalPowerWatts} W` },
  { label: '最高温度', value: `${gpuSummaryMetrics.value.maximumTemperatureCelsius} °C` },
])

const refreshJobGPUHistory = createSingleFlight(async () => {
  const id = String(route.params.id)
  try {
    const payload = await fetchJobGPUHistory(id, selectedGPUWindow.value)
    if (id !== String(route.params.id)) return
    gpuHistory.value = normalizeGPUHistory(payload)
    gpuMetricsError.value = ''
  } catch (error) {
    if (id !== String(route.params.id)) return
    gpuMetricsError.value = gpuHistory.value.devices.length
      ? 'GPU 指标刷新暂时失败；已加载的 GPU 历史仍可查看。'
      : 'GPU 指标暂时不可达，平台将在 30 秒后自动重试。'
  }
})
```

Watch the selected window, reset on job changes, start a separate 30-second timer on mount, and clear it on unmount:

```js
watch(selectedGPUWindow, refreshJobGPUHistory)
// inside the existing route watcher
gpuHistory.value = normalizeGPUHistory(null)
gpuMetricsError.value = ''
refreshJobGPUHistory()
// inside onMounted
refreshJobGPUHistory()
gpuRefreshTimer = window.setInterval(refreshJobGPUHistory, 30000)
// inside onUnmounted
window.clearInterval(gpuRefreshTimer)
```

- [ ] **Step 4: Render summary, explicit empty state, and four charts**

Under the existing metric cards, add a `训练 GPU` section containing:

```vue
<section class="space-y-4 rounded-2xl border border-slate-800/90 bg-slate-950/40 p-5">
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div>
      <h4 class="text-sm font-semibold text-slate-100">训练 GPU</h4>
      <p class="mt-1 text-xs text-slate-500">仅显示当前账号有权查看的这个训练任务实际使用的 Worker GPU</p>
    </div>
    <el-segmented v-model="selectedGPUWindow" :options="GPU_TIME_WINDOWS" size="small" />
  </div>
  <el-alert v-if="gpuMetricsError" :title="gpuMetricsError" type="warning" show-icon :closable="false" />
  <div v-if="gpuHistory.devices.length" class="space-y-4">
    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <div v-for="card in gpuStatCards" :key="card.label" class="rounded-xl border border-slate-800/80 bg-slate-900/60 p-4">
        <span class="text-xs text-slate-400">{{ card.label }}</span>
        <p class="mt-1 font-mono text-xl font-bold text-cyan-300">{{ card.value }}</p>
      </div>
    </div>
    <el-alert v-if="gpuSummaryMetrics.imbalanced" title="负载不均：卡间近 1 分钟利用率差值超过 30%，请检查数据加载、rank 阻塞或通信等待。" type="warning" show-icon :closable="false" />
    <el-alert v-if="gpuFreshness.stale" title="数据延迟：最新 GPU 样本超过 90 秒。训练任务不会因此停止。" type="info" show-icon :closable="false" />
    <div class="grid gap-4 xl:grid-cols-2">
      <GPUTrendChart title="GPU 使用率" unit="%" :series="gpuChartSeries('utilizationPercent')" :minimum="0" :maximum="100" />
      <GPUTrendChart title="显存使用量" unit="GiB" :series="gpuChartSeries('memoryUsedMib')" :minimum="0" :scale="1024" />
      <GPUTrendChart title="GPU 功率" unit="W" :series="gpuChartSeries('powerWatts')" :minimum="0" />
      <GPUTrendChart title="GPU 温度" unit="°C" :series="gpuChartSeries('temperatureCelsius')" :minimum="20" :maximum="100" />
    </div>
  </div>
  <el-empty v-else description="任务可能仍在排队、Worker 尚未启动，或历史指标已超过 Prometheus 保留期" />
</section>
```

- [ ] **Step 5: Run frontend tests and production build**

Run:

```bash
npm test
npm run build
```

Expected: all tests pass and Vite emits `dist/` without template or import errors.

- [ ] **Step 6: Commit the task-detail UI**

```bash
git add frontend/src/views/Job/JobDetail.vue frontend/src/jobGpuMetricsUi.test.js
git commit -m "feat: show GPU trends in training jobs"
```

### Task 5: User and operator documentation

**Files:**
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/OPERATIONS_GUIDE.md`

- [ ] **Step 1: Add the user-facing behavior contract**

Add this text to the task-observation section of `docs/USER_GUIDE.md`:

```markdown
### 任务 GPU 曲线

任务详情的“Loss 收敛曲线与指标”页会显示该任务 Worker 实际使用的每张 GPU：利用率、显存、功率和温度。普通用户只看到自己提交的任务；团队管理员看到本团队任务；平台管理员可以跨团队查看。任务排队或 Worker 尚未启动时没有 GPU 样本是正常现象。任务结束后曲线继续保留到 Prometheus 保留期结束；这不依赖 RayCluster 或 Pod 继续存在。
```

- [ ] **Step 2: Add the operator troubleshooting contract**

Add this text to the GPU metrics section of `docs/OPERATIONS_GUIDE.md`:

```markdown
任务级 GPU 曲线由后端使用数据库中的 `kubernetesNamespace` 和 `rayClusterName` 生成受控 Prometheus 查询，用户不能传入 Pod 正则或 PromQL。任务训练正常但曲线为空时，依次检查任务是否已观察到 RayCluster、Worker 是否启动、DCGM target 是否 Up、Prometheus 是否仍在保留窗口内。不要通过重启 RayJob、Worker、GPU 驱动或 FSX Agent 修复监控展示问题。
```

- [ ] **Step 3: Verify documentation links and formatting**

Run:

```bash
git diff --check
rg -n "任务 GPU 曲线|kubernetesNamespace.*rayClusterName" docs/USER_GUIDE.md docs/OPERATIONS_GUIDE.md
```

Expected: no whitespace errors and both new sections are found.

- [ ] **Step 4: Commit documentation**

```bash
git add docs/USER_GUIDE.md docs/OPERATIONS_GUIDE.md
git commit -m "docs: explain job GPU metric visibility"
```

### Task 6: Full verification and non-disruptive release handoff

**Files:**
- Verify only; no source changes expected.

- [ ] **Step 1: Run backend unit and integration tests**

Run:

```bash
go test ./...
```

Expected: every backend package passes.

- [ ] **Step 2: Run frontend tests and production build**

Run:

```bash
npm test
npm run build
```

Expected: every Node test passes and the production bundle builds.

- [ ] **Step 3: Run formatting, static, and secret checks**

Run:

```bash
gofmt -w backend/api/jobs.go backend/api/jobs_scoped_routes.go backend/api/jobs_gpu_metrics_test.go backend/observability/gpu_history.go backend/observability/gpu_history_test.go
go vet ./...
git diff --check
git diff --cached --check
rg -n "AKLT|glpat-|rpt_|12345678" --glob '!docs/archive/**' .
```

Expected: formatting and vet pass; the secret scan returns no active credentials.

- [ ] **Step 4: Review the complete branch diff**

Run:

```bash
git status --short
git log --oneline main..HEAD
git diff --stat main...HEAD
git diff main...HEAD
```

Expected: only the mapped files changed, each task has a focused commit, and no generated `frontend/dist` or dependency directory is tracked.

- [ ] **Step 5: Capture live training identities before deployment**

On the cluster administration machine run:

```bash
kubectl get rayjobs.ray.io -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,UID:.metadata.uid,STATE:.status.jobStatus,CLUSTER:.status.rayClusterName
kubectl get rayclusters.ray.io -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,UID:.metadata.uid
kubectl get pods -A -l ray.io/node-type=worker -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,UID:.metadata.uid,RESTARTS:.status.containerStatuses[0].restartCount
```

Save the output in the release terminal scrollback and identify every `RUNNING` task. The deployment must not patch or delete these resources.

- [ ] **Step 6: Build immutable backend and frontend images**

On the build machine from `/opt/guofeng/vke-cluster/ray-platform` run:

```bash
IMAGE_TAG="prod-$(date -u +%Y%m%d-%H%M%S)-job-gpu"
REGISTRY=harbor.wellspiking.ai/guofeng.su \
IMAGE_TAG="$IMAGE_TAG" BUILD_TARGETS=backend,frontend \
PUSH_IMAGE=true USE_BUILDX=true bash build-image.sh

BACKEND_IMAGE="harbor.wellspiking.ai/guofeng.su/ray-train-backend:${IMAGE_TAG}"
FRONTEND_IMAGE="harbor.wellspiking.ai/guofeng.su/ray-train-frontend:${IMAGE_TAG}"
BACKEND_DIGEST="$(docker buildx imagetools inspect "$BACKEND_IMAGE" | awk '$1 == "Digest:" {print $2; exit}')"
FRONTEND_DIGEST="$(docker buildx imagetools inspect "$FRONTEND_IMAGE" | awk '$1 == "Digest:" {print $2; exit}')"
test -n "$BACKEND_DIGEST" && test -n "$FRONTEND_DIGEST"
printf 'IMAGE_TAG=%s\nBACKEND_DIGEST=%s\nFRONTEND_DIGEST=%s\n' "$IMAGE_TAG" "$BACKEND_DIGEST" "$FRONTEND_DIGEST"
```

Expected: both immutable `sha256:` digests are printed.

- [ ] **Step 7: Perform an atomic Helm rolling update of only backend and frontend**

Update only these six profile fields in `deploy/profiles/vke-cpu-ha.yaml` with the values printed in Step 6:

```yaml
backend:
  image:
    repository: ray-train-backend
    tag: <IMAGE_TAG printed by Step 6>
    digest: <BACKEND_DIGEST printed by Step 6>
frontend:
  image:
    repository: ray-train-frontend
    tag: <IMAGE_TAG printed by Step 6>
    digest: <FRONTEND_DIGEST printed by Step 6>
```

Review that no other profile line changed, then run:

```bash
git diff -- deploy/profiles/vke-cpu-ha.yaml
bash ops/platform/preflight.sh --profile deploy/profiles/vke-cpu-ha.yaml
bash ops/platform/deploy.sh --profile deploy/profiles/vke-cpu-ha.yaml --timeout 15m
bash ops/platform/verify.sh --profile deploy/profiles/vke-cpu-ha.yaml
```

Expected: Helm atomic upgrade succeeds and both deployments become Available. RayJob, Kueue, DCGM, Prometheus, Loki, MLflow, FSX, and training image values remain unchanged.

- [ ] **Step 8: Verify job-scoped behavior with real roles**

Using browser/API sessions only:

1. The running task owner sees four curves and the expected GPU count.
2. Another engineer in the same team receives 404 for that task's GPU endpoint.
3. The team administrator sees the task curves.
4. The platform administrator sees the task curves across tenants.
5. The global physical GPU page remains forbidden to an engineer.
6. A transient backend/Prometheus failure does not erase already-rendered samples.

- [ ] **Step 9: Prove running training was not affected**

Compare the recorded RayJob, RayCluster, and Worker Pod UIDs with post-release values. Confirm the task status and log stream continued through the deployment and no training Pod restarted.

- [ ] **Step 10: Commit the reviewed production profile only after verification**

After Steps 8 and 9 pass, commit the two reviewed image references:

```bash
git add deploy/profiles/vke-cpu-ha.yaml
git commit -m "chore: record job GPU metrics release"
```
