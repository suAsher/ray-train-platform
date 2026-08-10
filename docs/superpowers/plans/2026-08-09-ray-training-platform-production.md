# Ray Training Platform Production Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将当前 Vue/Go 原型改造成可在火山云 VKE 上运行的生产版 Ray 训练平台，打通 Keycloak/LDAP 登录、训练任务、调试工作区、IDC PVC 存储、日志指标和验收部署。

**Architecture:** 使用无状态 Go API、带 Lease leader election 的 Go Reconciler、PostgreSQL、Kueue 和 KubeRay。RayJob/RayCluster 的 Kubernetes 状态是运行事实，数据库保存 JobSpec、期望状态、事件、审计和状态快照；Vue Portal 通过 Authorization Code + PKCE 登录已有 Keycloak。

**Tech Stack:** Go 1.24, Gin, GORM + PostgreSQL, client-go dynamic client/informers, KubeRay RayJob/RayCluster, Kueue, Vue 3, Element Plus, Keycloak OIDC, Loki, Prometheus/DCGM, Helm, VKE.

---

## 文件地图

### Backend

- Create `backend/cmd/platform/main.go`: API/controller 两种运行模式的入口。
- Create `backend/config/config.go`: 环境变量、生产必填项和默认值。
- Create `backend/auth/oidc.go`, `backend/auth/middleware.go`: Keycloak JWT/JWKS 校验、租户和角色映射。
- Create `backend/domain/job.go`, `backend/domain/workspace.go`, `backend/domain/events.go`: 平台领域类型和状态转换。
- Create `backend/repositories/postgres.go`, `backend/repositories/jobs.go`, `backend/repositories/workspaces.go`: PostgreSQL repository 接口和实现。
- Create `backend/db/migrations/0001_initial.up.sql`, `backend/db/migrations/0001_initial.down.sql`: 版本化 schema。
- Create `backend/k8s/client.go`, `backend/k8s/rayjob.go`, `backend/k8s/raycluster.go`, `backend/k8s/status.go`: Kubernetes/KubeRay 适配器。
- Create `backend/controller/reconciler.go`, `backend/controller/leader.go`, `backend/controller/watch.go`: outbox 消费、状态 Watch、leader election 和清理。
- Create `backend/logs/loki.go`, `backend/metrics/prometheus.go`: 受控日志查询、流式日志和指标代理。
- Modify `backend/controllers/job_controller.go`, `backend/services/k8s_service.go`, `backend/services/loki_service.go`, `backend/models/models.go`, `backend/main.go`: 删除模拟行为，改为调用新模块。
- Create `backend/tests/`: API、repository、状态映射和 reconciler 测试。

### Frontend

- Create `frontend/src/auth/keycloak.js`, `frontend/src/auth/guard.js`: PKCE 登录、退出和路由保护。
- Create `frontend/src/api/client.js`, `frontend/src/api/jobs.js`, `frontend/src/api/workspaces.js`, `frontend/src/api/admin.js`: 统一 API 客户端。
- Create `frontend/src/components/jobs/JobStatus.vue`, `frontend/src/components/jobs/JobLogViewer.vue`, `frontend/src/components/jobs/JobMetrics.vue`, `frontend/src/components/jobs/JobTopology.vue`。
- Modify `frontend/src/views/Job/CreateJob.vue`, `frontend/src/views/Job/index.vue`, `frontend/src/views/Job/JobDetail.vue`, `frontend/src/views/Devcenter/index.vue`, `frontend/src/views/QuotaManage/index.vue`, `frontend/src/views/DevicesManagement/index.vue`, `frontend/src/layout/Layout.vue`, `frontend/src/router/index.js`。
- Remove all production fallback data and hard-coded demo URLs.

### Kubernetes/Helm

- Create `helm/ray-train-platform/templates/controller-deployment.yaml`。
- Create `helm/ray-train-platform/templates/migrations-job.yaml`。
- Create `helm/ray-train-platform/templates/network-policy.yaml`。
- Create `helm/ray-train-platform/templates/idc-storage-pvc.yaml`。
- Modify `helm/ray-train-platform/templates/backend-deployment.yaml`, `helm/ray-train-platform/templates/rbac.yaml`, `helm/ray-train-platform/templates/vke-alb-ingress.yaml`, `helm/ray-train-platform/values.yaml`。
- Modify `k8s/templates/rayjob-template.yaml`, `k8s/templates/dev-raycluster-template.yaml` to use validated values, Secret references and PVC mounts。

### Verification and documentation

- Create `scripts/preflight-vke.sh`: VKE/KubeRay/Kueue/GPU/PVC/Keycloak preflight。
- Create `scripts/e2e-training.sh`: 1 GPU training lifecycle acceptance。
- Create `scripts/e2e-storage-auth.sh`: Keycloak and IDC PVC acceptance。
- Modify `docs/CLUSTER_DEPLOYMENT_GUIDE.md`: production deployment and rollback runbook。

---

### Task 1: Lock configuration, API contracts, and test harness

**Files:**
- Create: `backend/config/config.go`, `backend/config/config_test.go`
- Create: `backend/domain/job.go`, `backend/domain/job_test.go`, `backend/domain/events.go`, `backend/domain/workspace.go`
- Create: `backend/httpapi/envelope.go`, `backend/httpapi/request_id.go`
- Modify: `backend/main.go`, `backend/go.mod`

- [ ] **Step 1: Define production configuration and fail-fast validation**

Implement `config.Config` with these fields and environment names: `HTTP_ADDR`, `DATABASE_URL`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_AUDIENCE`, `OIDC_REQUIRED`, `KUBECONFIG`, `KUBE_CONTEXT`, `LOKI_URL`, `PROMETHEUS_URL`, `IDC_STORAGE_ENABLED`, `IDC_EXISTING_CLAIM`, `IDC_STORAGE_CLASS`, `IDC_MOUNT_PATH`, `RAY_VERSION`, `RAY_IMAGE_ALLOWLIST`, `GIT_ALLOWLIST`, `TOS_BUCKET`, `DEMO_MODE`.

`Load()` must reject production startup when `DATABASE_URL`, OIDC values, and Kubernetes configuration are missing. `DEMO_MODE=true` must be rejected when `APP_ENV=production`.

- [ ] **Step 2: Write tests for configuration boundaries and state transitions**

Tests must assert missing production configuration returns named errors, demo mode is rejected in production, and valid state transitions include `SUBMITTED -> VALIDATING -> QUEUED -> ADMITTED -> PROVISIONING -> RUNNING -> SUCCEEDED/FAILED/CANCELED` while `SUCCEEDED -> RUNNING` is rejected.

Run: `cd backend && go test ./config ./domain`
Expected: initial test failures before the implementation is complete, then PASS.

- [ ] **Step 3: Implement the API envelope and request ID middleware**

Use this response type:

```go
type Envelope[T any] struct {
    Success   bool   `json:"success"`
    Data      T      `json:"data,omitempty"`
    Error     *Error `json:"error,omitempty"`
    RequestID string `json:"request_id"`
}
```

Every handler must return the request ID from `X-Request-ID` or generate a UUID; errors must not expose SQL, token, or Kubernetes credentials.

- [ ] **Step 4: Run the backend baseline checks**

Run: `cd backend && gofmt -w . && go test ./...`
Expected: compilation succeeds for the new packages; old fake-service tests are not added back.

---

### Task 2: Replace SQLite with PostgreSQL migrations and repositories

**Files:**
- Create: `backend/db/migrations/0001_initial.up.sql`, `backend/db/migrations/0001_initial.down.sql`
- Create: `backend/repositories/postgres.go`, `backend/repositories/jobs.go`, `backend/repositories/workspaces.go`, `backend/repositories/events.go`
- Create: `backend/repositories/jobs_test.go`
- Modify: `backend/models/models.go`, `backend/services/k8s_service.go`, `backend/go.mod`

- [ ] **Step 1: Add PostgreSQL and migration dependencies**

Add `gorm.io/driver/postgres` and `github.com/golang-migrate/migrate/v4` to `backend/go.mod`. Remove `gorm.io/driver/sqlite` from the production path.

- [ ] **Step 2: Create the schema migration**

Create tables `tenants`, `users`, `training_jobs`, `job_events`, `job_artifacts`, `dev_workspaces`, `audit_logs`, `idempotency_keys`, and `outbox_events` with UUID/string primary keys, tenant indexes, status indexes, `created_at`, `updated_at`, and foreign keys where ownership is required.

`training_jobs` must include `spec_json`, `desired_state`, `observed_state`, `status_reason`, `k8s_namespace`, `rayjob_name`, `rayjob_uid`, `raycluster_name`, `resource_version`, `retry_count`, `timeout_seconds`, `cleanup_policy_json`, and `last_observed_at`.

- [ ] **Step 3: Implement repository interfaces before handlers**

Expose these interfaces:

```go
type JobRepository interface {
    Create(ctx context.Context, job *domain.TrainingJob, key string) error
    Get(ctx context.Context, tenantID, jobID string) (*domain.TrainingJob, error)
    List(ctx context.Context, filter domain.JobFilter) (domain.Page[domain.TrainingJob], error)
    SetDesiredState(ctx context.Context, tenantID, jobID string, state domain.DesiredState) error
    ApplyObservedState(ctx context.Context, observed domain.ObservedJobState) error
}

type OutboxRepository interface {
    ClaimBatch(ctx context.Context, limit int) ([]domain.OutboxEvent, error)
    MarkDone(ctx context.Context, id string) error
    MarkRetry(ctx context.Context, id string, nextAttempt time.Time, reason string) error
}
```

Use transactions and `FOR UPDATE SKIP LOCKED` for outbox claims. Enforce tenant filtering in repository queries, not only in the UI.

- [ ] **Step 4: Add repository tests**

Test duplicate `Idempotency-Key`, tenant isolation, outbox claim/retry, observed-state update, and pagination. Use a PostgreSQL test database when `TEST_DATABASE_URL` is set; otherwise run SQL shape tests with `go-sqlmock`.

Run: `cd backend && go test ./repositories -v`
Expected: PASS with tenant B unable to read tenant A data.

---

### Task 3: Integrate existing Keycloak and LDAP-backed identity

**Files:**
- Create: `backend/auth/oidc.go`, `backend/auth/middleware.go`, `backend/auth/claims.go`, `backend/auth/oidc_test.go`
- Create: `frontend/src/auth/keycloak.js`, `frontend/src/auth/guard.js`
- Modify: `frontend/package.json`, `frontend/src/router/index.js`, `frontend/src/layout/Layout.vue`, `backend/main.go`, `backend/config/config.go`

- [ ] **Step 1: Configure Keycloak as the only login authority**

Add `keycloak-js` to the frontend. Configure an existing Keycloak public client for Authorization Code + PKCE with exact redirect URIs for the Portal host, silent check-sso URI, and post-logout URI. LDAP users are synchronized by Keycloak User Federation; no platform endpoint accepts LDAP passwords.

- [ ] **Step 2: Implement frontend auth initialization and route guards**

`frontend/src/auth/keycloak.js` must expose `initAuth()`, `login()`, `logout()`, `getToken()`, and `currentUser()`. Store the access token in memory, refresh it before expiry, and attach it to API calls through `frontend/src/api/client.js`. `guard.js` must redirect unauthenticated users to Keycloak and prevent tenant-admin pages for engineer roles.

- [ ] **Step 3: Implement backend JWT/JWKS validation**

Use the OIDC issuer discovery document and JWKS to validate signature, issuer, audience, expiry, and nonce/state. Map claims using configurable paths: `preferred_username`, `email`, `groups`, and `realm_access.roles`. Require a stable Keycloak subject as the platform user ID.

The middleware must put this context into the request:

```go
type Principal struct {
    Subject  string
    Username string
    Email    string
    TenantID  string
    Roles    []string
}
```

- [ ] **Step 4: Test Keycloak claim mapping and authorization**

Test valid claims, wrong issuer, wrong audience, expired token, missing subject, group-to-tenant mapping, and role checks for `SuperAdmin`, `TenantAdmin`, and `Engineer`.

Run: `cd backend && go test ./auth -v`
Expected: PASS; no test uses a real client secret.

---

### Task 4: Implement Kubernetes/KubeRay adapters and real reconciliation

**Files:**
- Create: `backend/k8s/client.go`, `backend/k8s/discovery.go`, `backend/k8s/rayjob.go`, `backend/k8s/raycluster.go`, `backend/k8s/status.go`
- Create: `backend/controller/reconciler.go`, `backend/controller/leader.go`, `backend/controller/watch.go`, `backend/controller/reconciler_test.go`
- Modify: `backend/go.mod`, `backend/cmd/platform/main.go`, `backend/main.go`, `k8s/templates/rayjob-template.yaml`, `k8s/templates/dev-raycluster-template.yaml`

- [ ] **Step 1: Add client-go and CRD discovery**

Use `client-go/dynamic`, typed Kubernetes core clients, and discovery. At controller startup, verify `rayjobs.ray.io`, `rayclusters.ray.io`, `workloads.kueue.x-k8s.io`, required API versions, and the installed KubeRay CRD schema. Fail readiness with a clear error when the schema does not support the configured template.

- [ ] **Step 2: Implement a typed internal RayJob renderer**

Render from validated `domain.TrainingJob`, not from raw user YAML. Include labels `platform.ray.io/job-id`, `platform.ray.io/tenant-id`, `platform.ray.io/managed-by`, Kueue LocalQueue label, `spec.suspend: true`, `shutdownAfterJobFinishes: true`, success/failure TTL cleanup, exact worker/head resources, node labels, taints, topology spread, PVC references, Secret references, and no literal TOS access keys.

The renderer must reject empty name, invalid DNS label, zero workers, GPU count above tenant quota, unapproved image, unapproved Git host, arbitrary hostPath, and arbitrary node selector.

- [ ] **Step 3: Implement idempotent create/update/delete**

The reconciler must:

1. Claim an outbox event.
2. Load the tenant-scoped job.
3. Create or update exactly one RayJob using platform labels and owner references.
4. Persist CR UID/resourceVersion.
5. Delete or suspend only the owned resource when desired state is `CANCELED`.
6. Mark the outbox done only after the Kubernetes action and database update succeed.

- [ ] **Step 4: Implement watches and state mapping**

Map RayJob conditions, Kueue admission, RayCluster readiness, submitter Job, Pod phases, container termination reasons, and Kubernetes Events into `ObservedJobState`. Preserve the raw reason and message for the detail page.

- [ ] **Step 5: Implement leader election and periodic resync**

Use a Kubernetes Lease in `ray-platform-system`; only the elected controller consumes outbox and performs writes. Run a full tenant-scoped resync every 60 seconds to recover from missed events.

- [ ] **Step 6: Test with fake dynamic clients**

Test create idempotency, update after controller restart, Kueue queued/admitted mapping, RayJob success/failure, Pod OOM, cancellation cleanup, resourceVersion conflict retry, and CRD discovery failure.

Run: `cd backend && go test ./k8s ./controller -v`
Expected: PASS without requiring a live cluster.

---

### Task 5: Replace fake API behavior with production endpoints

**Files:**
- Create: `backend/httpapi/jobs.go`, `backend/httpapi/workspaces.go`, `backend/httpapi/admin.go`
- Modify: `backend/controllers/job_controller.go`, `backend/services/k8s_service.go`, `backend/services/loki_service.go`, `backend/main.go`
- Create: `backend/httpapi/jobs_test.go`, `backend/httpapi/workspaces_test.go`

- [ ] **Step 1: Implement authenticated job submission**

`POST /api/v1/jobs` must accept JobSpec, derive user/tenant from `Principal`, validate resources and source, create the DB row and outbox event in one transaction, and return HTTP 202 with job ID and current state.

- [ ] **Step 2: Implement list/detail/event/cancel/retry endpoints**

Every query must apply tenant scope from `Principal`. Cancel changes desired state; retry creates a new attempt while retaining the original attempt event history. No endpoint may return a fake default job when the ID is absent.

- [ ] **Step 3: Implement real topology and capacity queries**

Read owned RayCluster/Pod state and cluster nodes/GPU capacity through the Kubernetes adapter. Use Prometheus/DCGM data for utilization and memory; return `unknown` instead of invented numbers when metrics are unavailable.

- [ ] **Step 4: Implement real workspace lifecycle endpoints**

Create/update/delete one tenant-scoped RayCluster per workspace, create an internal Service for Jupyter, issue a short-lived access URL, persist TTL, and reject direct Pod IP exposure. Support `/workspaces/{id}/snapshot` with a versioned workspace artifact record.

- [ ] **Step 5: Remove all fake production paths**

Delete seed data, simulated topology, synthetic WebSocket log generation, fake Jupyter URLs, and frontend fallback job data. Gate any local demo fixture behind `DEMO_MODE=true` and fail production startup if demo mode is enabled.

- [ ] **Step 6: Test API authorization and lifecycle**

Run: `cd backend && go test ./httpapi -v`
Expected: PASS for submit/list/detail/cancel/retry/workspace and tenant isolation.

---

### Task 6: Implement Loki logs and Prometheus/DCGM metrics

**Files:**
- Create: `backend/logs/loki.go`, `backend/logs/loki_test.go`, `backend/metrics/prometheus.go`, `backend/metrics/prometheus_test.go`
- Create: `frontend/src/components/jobs/JobLogViewer.vue`, `frontend/src/components/jobs/JobMetrics.vue`
- Modify: `backend/httpapi/jobs.go`, `frontend/src/views/Job/JobDetail.vue`, `helm/ray-train-platform/values.yaml`

- [ ] **Step 1: Build server-owned Loki queries**

Generate LogQL only from platform job ID, tenant namespace, Pod, rank, component, time range, and keyword. Reject user-supplied raw LogQL. Cap query range and response size.

- [ ] **Step 2: Implement authenticated log streaming**

Keep the existing WebSocket route for compatibility, but replace synthetic output with Loki tail/query polling. Add heartbeat, close handling, reconnect cursor, server-side rate limit, and log export size limit.

- [ ] **Step 3: Implement metrics query adapters**

Query Prometheus for GPU utilization, memory, temperature, node health, queue wait, Ray cluster health, and task metrics. Return metric timestamp and `stale` status when the last sample exceeds the configured freshness window.

- [ ] **Step 4: Add UI log/metric states**

The detail page must show loading, empty, stale, permission denied, backend unavailable, and live states. It must support node/Pod/rank filters without exposing raw query syntax.

- [ ] **Step 5: Test log query safety and stream behavior**

Run: `cd backend && go test ./logs ./metrics -v`
Expected: malicious LogQL input is rejected, tenant labels are always included, and stale metrics are marked.

---

### Task 7: Add IDC PVC and data mount support

**Files:**
- Create: `helm/ray-train-platform/templates/idc-storage-pvc.yaml`
- Modify: `helm/ray-train-platform/values.yaml`, `helm/ray-train-platform/templates/backend-deployment.yaml`, `helm/ray-train-platform/templates/controller-deployment.yaml`, `k8s/templates/rayjob-template.yaml`, `k8s/templates/dev-raycluster-template.yaml`, `docs/CLUSTER_DEPLOYMENT_GUIDE.md`
- Create: `scripts/preflight-vke.sh`

- [ ] **Step 1: Define explicit storage values**

Add `storage.idc.enabled`, `storage.idc.existingClaim`, `storage.idc.storageClassName`, `storage.idc.accessMode`, `storage.idc.mountPath`, `storage.idc.readOnly`, `storage.workspace.subPathPattern`, `storage.localCacheHostPath`, and `storage.localSpillHostPath`.

- [ ] **Step 2: Render PVC references safely**

Use `existingClaim` when provided; otherwise render a PVC only when `storageClassName` and capacity are present. Never render hostPath from user JobSpec. Dataset mounts are read-only; workspace mounts are tenant-scoped read/write.

- [ ] **Step 3: Add PVC readiness to submission validation**

Before a job enters `QUEUED`, verify the referenced PVC exists, is bound, is in the tenant namespace, and has the requested access mode. A missing/unbound PVC produces a user-readable validation event and does not create a RayJob.

- [ ] **Step 4: Add IDC/VKE preflight checks**

`scripts/preflight-vke.sh` must check Kubernetes context, KubeRay CRDs, Kueue resources, NVIDIA nodes/device plugin, DCGM exporter, IDC PVC bound state, mount test Pod, DNS, route/port reachability, and Ray image pull.

Run: `bash scripts/preflight-vke.sh`
Expected: a named PASS/FAIL table with no successful result when the IDC PVC or route is unavailable.

---

### Task 8: Complete the production Vue UI

**Files:**
- Create: `frontend/src/api/client.js`, `frontend/src/api/jobs.js`, `frontend/src/api/workspaces.js`, `frontend/src/api/admin.js`
- Create: `frontend/src/components/jobs/JobStatus.vue`, `frontend/src/components/jobs/JobTopology.vue`
- Modify: `frontend/src/views/Job/CreateJob.vue`, `frontend/src/views/Job/index.vue`, `frontend/src/views/Job/JobDetail.vue`, `frontend/src/views/Devcenter/index.vue`, `frontend/src/views/QuotaManage/index.vue`, `frontend/src/views/DevicesManagement/index.vue`, `frontend/src/layout/Layout.vue`, `frontend/src/router/index.js`, `frontend/src/index.css`, `frontend/package.json`
- Create: `frontend/src/api/client.test.js`, `frontend/src/components/jobs/JobStatus.test.js`

- [ ] **Step 1: Replace direct fetch calls with one API client**

The client must attach the in-memory Keycloak token, propagate `X-Request-ID`, parse the common envelope, map 401 to re-login, map 403 to permission UI, and show backend request IDs in error details.

- [ ] **Step 2: Make the task wizard submit the versioned JobSpec**

Validate name, image, Git commit, entrypoint, GPU count, CPU/memory, queue, dataset URI, output URI, retry and cleanup policy before POST. Display total resource calculation and a pre-submit summary.

- [ ] **Step 3: Build real job list/detail states**

Add pagination, filters, queue wait duration, multi-layer status, event timeline, topology, logs, metrics, artifacts, cancel and retry. Poll detail until terminal or subscribe to the stream endpoint; never invent a value when the API omits it.

- [ ] **Step 4: Build real Debug Center**

Show workspace state, GPU assignment, Jupyter action, idle TTL, stop/restart, snapshot, and conversion to JobSpec using the saved snapshot ID. Disable actions based on server authorization and workspace state.

- [ ] **Step 5: Build admin UI**

Show tenant queues, quotas, users/groups/roles from the platform API, node/GPU health, and audit logs. Hide admin routes and controls for non-admin roles.

- [ ] **Step 6: Verify the UI**

Run: `cd frontend && npm run build`
Expected: production build succeeds with no fake fallback imports, no hard-coded `localhost` Jupyter URL, and no demo token.

---

### Task 9: Harden Helm deployment and network policy

**Files:**
- Create: `helm/ray-train-platform/templates/controller-deployment.yaml`, `helm/ray-train-platform/templates/migrations-job.yaml`, `helm/ray-train-platform/templates/network-policy.yaml`
- Modify: `helm/ray-train-platform/values.yaml`, `helm/ray-train-platform/templates/backend-deployment.yaml`, `helm/ray-train-platform/templates/rbac.yaml`, `helm/ray-train-platform/templates/vke-alb-ingress.yaml`, `helm/ray-train-platform/templates/frontend-deployment.yaml`
- Modify: `backend/Dockerfile`, `frontend/Dockerfile`

- [ ] **Step 1: Split API and controller deployments**

Use the same image with `--mode=api` and `--mode=controller`. API has at least two replicas; controller has two replicas with Lease leader election. Add non-root security context, read-only root filesystem, resource requests/limits, probes, PDB, and topology spread.

- [ ] **Step 2: Add migration job and production values**

Run migrations before API rollout. Production values must reference existing Secrets for database, Keycloak, registry pull, TOS and monitoring. No secret value is committed to Helm or source files.

- [ ] **Step 3: Reduce RBAC and add NetworkPolicy**

Grant the API only namespaced read access needed for query endpoints. Grant the controller create/update/delete only for owned RayJob/RayCluster/Pod-related resources and read access to Kueue status, Events, Nodes and metrics services. Add default-deny NetworkPolicy and explicit egress to Kubernetes API, PostgreSQL, Keycloak, Loki, Prometheus, TOS/NAS endpoints.

- [ ] **Step 4: Fix private ingress and WebSocket routing**

Configure private ALB, TLS, `/api`, `/ws`, `/proxy` routes, idle timeout, origin allowlist and no public dashboard/Jupyter service. Add frontend SPA fallback and backend readiness checks.

- [ ] **Step 5: Render and lint manifests**

Run: `helm lint helm/ray-train-platform`
Run: `helm template ray-platform helm/ray-train-platform --namespace ray-platform-system --set appEnv=staging`
Expected: no template errors, no literal secret values, API/controller services and required RBAC are present.

---

### Task 10: Add observability deployment and production runbook

**Files:**
- Create: `k8s/observability/dcgm-servicemonitor.yaml`, `k8s/observability/ray-dashboard-servicemonitor.yaml`, `k8s/observability/alerts.yaml`
- Modify: `docs/CLUSTER_DEPLOYMENT_GUIDE.md`
- Create: `docs/PRODUCTION_RUNBOOK.md`, `docs/KEYCLOAK_SETUP.md`, `docs/IDC_STORAGE_SETUP.md`

- [ ] **Step 1: Add Prometheus alert rules**

Create alerts for GPU XID, GPU unavailable, node NotReady, queue wait threshold, RayCluster not ready, Pod CrashLoop/OOM, NCCL timeout pattern, TOS/PVC errors, stale checkpoints, API 5xx and controller leader absence.

- [ ] **Step 2: Document Keycloak setup**

Document client type, Authorization Code + PKCE, valid redirect URIs, post-logout URI, group/role claims, LDAP Federation mapping, tenant group naming, platform role mapping, token audience, and logout behavior. Do not document or store LDAP passwords in this repository.

- [ ] **Step 3: Document IDC storage setup**

Document the required VKE-to-IDC route, StorageClass or precreated PV/PVC, access mode, mount path, tenant directory layout, read-only dataset policy, read/write workspace policy, capacity test and rollback procedure.

- [ ] **Step 4: Document operations**

Include deployment order, preflight command, migration, Helm install/upgrade/rollback, controller failover, stuck RayJob cleanup, log retention, local NVMe cleanup, PostgreSQL backup/restore, Keycloak outage behavior, and IDC storage outage behavior.

---

### Task 11: Run end-to-end acceptance on VKE

**Files:**
- Create: `scripts/e2e-training.sh`, `scripts/e2e-storage-auth.sh`, `scripts/e2e-failure.sh`
- Modify: `docs/PRODUCTION_RUNBOOK.md`

- [ ] **Step 1: Run static checks**

Run:

```bash
cd backend && gofmt -w . && go test ./... && go vet ./...
cd ../frontend && npm run build
cd .. && helm lint helm/ray-train-platform
```

Expected: all commands exit 0.

- [ ] **Step 2: Run Keycloak/LDAP and IDC storage acceptance**

Run `bash scripts/e2e-storage-auth.sh` with the staging Keycloak issuer, realm, client ID, tenant group, and existing IDC PVC configured through environment variables. Verify login, LDAP-synced group mapping, tenant isolation, read-only dataset mount, read/write workspace mount, and explicit failure when the PVC is unavailable.

- [ ] **Step 3: Run 1-GPU lifecycle acceptance**

Run `bash scripts/e2e-training.sh` to submit a pinned image and Git commit, assert `QUEUED -> ADMITTED -> RUNNING -> SUCCEEDED`, read Loki logs, read Prometheus/DCGM metrics, verify Checkpoint URI, and assert RayCluster cleanup.

- [ ] **Step 4: Run 24-GPU and failure acceptance**

Submit a 3-worker × 8-GPU job, run NCCL tests, cancel a queued job, inject Worker OOM, restart a node, stop the controller leader, and verify that the UI reports the correct reason and the next controller converges without creating a duplicate RayJob.

- [ ] **Step 5: Record the production gate**

Only after all acceptance scripts pass should `APP_ENV=production` and `OIDC_REQUIRED=true` be enabled in the production values. Record cluster versions, Ray image digest, GPU driver/CUDA versions, NCCL measurements, storage capacity and backup restore result in `docs/PRODUCTION_RUNBOOK.md`.

---

## Self-review checklist

- [ ] UI, Keycloak/LDAP federation, IDC PVC, Kueue/KubeRay, logs, metrics, security, backups and VKE acceptance each have a task.
- [ ] No production step depends on fake data, SQLite, demo tokens, arbitrary hostPath or direct LDAP passwords.
- [ ] API, controller, repository, frontend, Helm and verification file paths match the file map.
- [ ] The plan keeps V1 on Kueue + KubeRay and does not introduce an unneeded custom Training Operator.
- [ ] Every production claim is gated by real VKE acceptance rather than local mock success.

