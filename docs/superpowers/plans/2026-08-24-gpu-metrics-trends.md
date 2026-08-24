# GPU Metrics Trends Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver accurate, explainable per-GPU current readings and historical curves without touching running training resources.

**Architecture:** Extend the Prometheus adapter with bounded range queries and immutable GPU history models, expose a fixed-parameter API, then render node-scoped ECharts panels and richer device cards. Keep the VKE-managed DCGM sampling configuration unchanged during active training.

**Tech Stack:** Go, Gin, Prometheus HTTP API, Vue 3, Element Plus, Apache ECharts, Node test runner.

---

### Task 1: Preserve Prometheus labels and timestamps

**Files:**
- Modify: `backend/observability/prometheus.go`
- Modify: `backend/observability/gpu.go`
- Test: `backend/observability/prometheus_test.go`
- Test: `backend/observability/gpu_test.go`

- [ ] Write failing tests that require range-series labels and instant-sample timestamps.
- [ ] Run focused observability tests and verify the assertions fail.
- [ ] Add immutable labels to `MetricSeries`, timestamps to instant samples, and workload/sample metadata to `GPUDevice`.
- [ ] Re-run focused tests and verify they pass.

### Task 2: Add bounded GPU history queries

**Files:**
- Create: `backend/observability/gpu_history.go`
- Create: `backend/observability/gpu_history_test.go`
- Modify: `backend/observability/prometheus.go`

- [ ] Write failing tests for the five allowed windows, fixed step sizes, UUID joins and missing-series behavior.
- [ ] Run `go test ./observability -run GPUHistory -count=1` and verify failure.
- [ ] Implement fixed PromQL templates and `QueryGPUHistory`; reject unsupported windows and arbitrary expressions.
- [ ] Re-run focused tests and verify they pass.

### Task 3: Expose the history API

**Files:**
- Modify: `backend/api/session.go`
- Test: `backend/api/gpu_metrics_test.go`

- [ ] Write failing API tests for authentication, valid windows and invalid-window rejection.
- [ ] Run `go test ./api -run GPUHistory -count=1` and verify failure.
- [ ] Add `GET /api/v1/cluster/gpu-metrics/history?window=<allowed>&node=<optional>` using server-owned query templates.
- [ ] Re-run focused API tests and verify they pass.

### Task 4: Build tested frontend metric models

**Files:**
- Create: `frontend/src/gpuMetrics.js`
- Create: `frontend/src/gpuMetrics.test.js`
- Modify: `frontend/src/api/platform.js`

- [ ] Write failing tests for freshness, node summaries, imbalance detection, allowed ranges and ECharts series conversion.
- [ ] Run `npm test -- gpuMetrics.test.js` and verify failure.
- [ ] Implement pure immutable helpers and the typed history API call.
- [ ] Re-run the focused test and verify it passes.

### Task 5: Render trends and richer device cards

**Files:**
- Create: `frontend/src/components/gpu/GPUTrendChart.vue`
- Modify: `frontend/src/views/DevicesManagement/index.vue`
- Modify: `frontend/package.json`
- Modify: `frontend/package-lock.json`
- Test: `frontend/src/gpuMetricsUi.test.js`

- [ ] Write a failing UI source-contract test for the four charts, time selector, sample freshness, workload owner and imbalance warning.
- [ ] Add the tree-shaken ECharts dependency and a responsive line-chart component with tooltip, legend and gap preservation.
- [ ] Add resource-pool, selected-node trend and per-device detail sections without changing existing routes or permissions.
- [ ] Run the focused UI test, all frontend tests and the production build.

### Task 6: Verify and release without training disruption

**Files:**
- Modify only immutable backend/frontend image references in the production profile.

- [ ] Run formatting, `go test ./...`, `go vet ./...`, `npm test`, `npm run build` and `git diff --check`.
- [ ] Record the running RayJob UID/state and all training Pod UIDs before deployment.
- [ ] Build and push immutable backend/frontend images, then perform the existing Helm rolling upgrade.
- [ ] Verify both platform replicas are Ready, current/history APIs return valid samples and curves, and the recorded RayJob/Pod UIDs remain unchanged.
- [ ] Commit and push the release only after the zero-disruption comparison passes.
