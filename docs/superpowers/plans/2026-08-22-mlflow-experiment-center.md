# MLflow Experiment Center Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent, tenant-scoped MLflow experiment center and verify all three BEVFusion submission methods with real users.

**Architecture:** The backend is the sole MLflow client and derives filters from the authenticated principal. The frontend renders a durable experiment catalog and links to existing job details; raw MLflow and artifact APIs remain private.

**Tech Stack:** Go/Gin, Vue 3, MLflow REST API, Kubernetes, KubeRay, Helm, Playwright/Node tests.

---

### Task 1: Tenant-scoped experiment catalog

**Files:**
- Modify: `backend/observability/mlflow.go`
- Modify: `backend/observability/mlflow_test.go`
- Modify: `backend/api/jobs.go`
- Create: `backend/api/experiments_test.go`
- Modify: `backend/api/jobs_scoped_routes.go`

- [ ] Write failing tests for tenant experiment lookup, owner filtering, admin visibility, limits, and omission of artifact fields.
- [ ] Run `go test ./observability ./api` and confirm the new tests fail for missing catalog behavior.
- [ ] Implement `ListTenantExperiments` and `GET /api/v1/experiments` using server-generated filters.
- [ ] Run `go test ./observability ./api` and confirm all tests pass.

### Task 2: Persistent experiment center UI

**Files:**
- Create: `frontend/src/views/Experiments/index.vue`
- Create: `frontend/src/experimentCatalog.js`
- Create: `frontend/src/experimentCatalog.test.js`
- Modify: `frontend/src/router/index.js`
- Modify: `frontend/src/layout/Layout.vue`

- [ ] Write failing unit tests for metric selection, status labels, duration, and no-download presentation.
- [ ] Run frontend tests and confirm the new tests fail.
- [ ] Add the authenticated `/experiments` route and navigation item.
- [ ] Render run cards/table with task links and no artifact/download actions.
- [ ] Run frontend unit tests and production build.

### Task 3: Runtime client and deployment verification

**Files:**
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `ops/mlflow/40-smoke.yaml`

- [ ] Verify corrected train/workspace images import MLflow 3.14.0.
- [ ] Pin corrected image digests and deploy the platform with FSX IRSA preflight.
- [ ] Run MLflow contract, health, database, TOS artifact and NetworkPolicy checks.

### Task 4: Fresh-clone acceptance matrix

**Files:**
- Modify only documented BEVFusion checkout files in the remote acceptance directory.
- Update: `docs/BEVFUSION_END_TO_END_GUIDE.md`
- Create: `docs/acceptance/MLFLOW_AND_SUBMISSION_ACCEPTANCE_20260822.md`

- [ ] Clone both documented branches into a new timestamped directory and apply only the guide's documented changes.
- [ ] Run data preflight against `/mnt/storage/public` and record the resolved `labeled` dataset evidence.
- [ ] As `guofeng.su`, complete spk-rayjob, native Ray Jobs API, and Portal snapshot submissions sequentially.
- [ ] Create a temporary Engineer account without printing its password, run one complete isolated job, verify experiment visibility, then delete the account and its test resources.
- [ ] Verify each task appears in the Portal and experiment center, has logs/metrics, writes checkpoint/output to governed storage, and leaves no exposed credentials.
- [ ] Update the guide only from observed behavior and attach task IDs, states, topology, MLflow run IDs and cleanup results to the acceptance report.
