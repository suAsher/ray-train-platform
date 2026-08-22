# Production Training Platform Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver governed data, 1/8/16-GPU Ray training, persistent GPU debugging, production observability, and repeatable production deployment.

**Architecture:** Production Ray Pods target the two labelled eight-GPU nodes and are admitted by Kueue. TOS/FSX is the durable user storage layer, NFS personal data is mediated by a trusted control-plane service and scoped transfer Jobs, and local CSI NVMe volumes are ephemeral cache only. Portal and API remain CPU-node HA services; logs use Loki while Prometheus/DCGM serves metrics.

**Tech Stack:** Go/Gin/GORM, Vue 3/Element Plus, Helm, Kubernetes/KubeRay/Kueue, Loki/Alloy, Prometheus/DCGM, TOS FSX CSI, NFS, Harbor.

---

## Delivery order and external prerequisites

Tasks 1–3 have no additional external dependency and are the first implementation batch. Task 4 needs the operator-provided personal NFS export/template to activate but can be implemented and render-tested behind `enabled: false`. Task 5 image self-build needs a scoped Harbor robot secret; approved-image selection remains fully usable without it.

### Task 1: Create a production GPU pool contract

**Files:**
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/templates/kueue-resources.yaml`
- Modify: `backend/config/config.go`
- Modify: `backend/config/config_test.go`
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_cache_test.go`
- Create: `ops/gpu/label-production-pool.sh`
- Create: `ops/gpu/verify-production-pool.sh`

- [ ] **Step 1: Write failing configuration tests for a two-node production pool.**

```go
func TestLoadAcceptsProductionPoolSelectorAndSixteenGPUCeiling(t *testing.T) {
    t.Setenv("TRAINING_NODE_SELECTOR", "accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production")
    t.Setenv("MAX_WORKER_REPLICAS", "2")
    t.Setenv("MAX_GPUS_PER_WORKER", "8")
    t.Setenv("MAX_TOTAL_GPUS", "16")
    cfg, err := Load()
    if err != nil { t.Fatal(err) }
    if cfg.TrainingNodeSelector["platform.wellspiking.ai/gpu-pool"] != "production" || cfg.MaxTotalGPUs != 16 { t.Fatal("production pool contract lost") }
}
```

- [ ] **Step 2: Run the focused test and confirm it fails before profile support is added.**

Run: `cd backend && go test ./config -run TestLoadAcceptsProductionPoolSelectorAndSixteenGPUCeiling -count=1`

- [ ] **Step 3: Make the production profile explicit.** Set production selector `accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production`, `maxWorkerReplicas: 2`, `maxGPUsPerWorker: 8`, `maxTotalGPUs: 16`, and Kueue GPU quota 16. Add the same safe defaults to `values.yaml` only when deployment profile values override them.

- [ ] **Step 4: Add idempotent node labelling and read-only verification scripts.**

```bash
kubectl label node welldriver-002 welldriver-003 platform.wellspiking.ai/gpu-pool=production --overwrite
kubectl label node <legacy-test-node> platform.wellspiking.ai/gpu-pool=legacy-test --overwrite
kubectl get nodes -l platform.wellspiking.ai/gpu-pool=production -o custom-columns=NAME:.metadata.name,GPUS:.status.allocatable.nvidia\\.com/gpu
```

The verification script must require exactly two selected nodes and a total of 16 allocatable GPUs before returning success.

- [ ] **Step 5: Run focused and package tests.**

Run: `cd backend && go test ./config ./k8s -count=1`

- [ ] **Step 6: Render the production chart.**

Run: `helm template ray-train-platform helm/ray-train-platform -f deploy/profiles/vke-cpu-ha.yaml >/tmp/ray-platform-production.yaml`

Expected: Ray backend environment contains both selector labels and the Kueue ResourceFlavor requires the production pool label.

### Task 2: Add production metrics and job observability

**Files:**
- Create: `ops/observability/prometheus/20-values-cpu-ha.yaml`
- Create: `ops/observability/prometheus/deploy-ha.sh`
- Create: `ops/observability/prometheus/verify.sh`
- Create: `ops/observability/grafana/20-dashboard-ray-training.json`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: Write a failing verification fixture for the Prometheus deployment values.** It must assert CPU-node affinity, two Prometheus replicas only when the selected chart supports HA, a persistent EBS claim, a ServiceMonitor selector that matches `app=nvidia-dcgm-exporter`, and `90d` retention.

- [ ] **Step 2: Run the verifier and confirm it fails because the values file does not exist.**

Run: `bash ops/observability/prometheus/verify.sh --render-only`

- [ ] **Step 3: Implement immutable Helm values and deployment verification.** The deploy script must use a chart version variable, `--atomic --wait`, CPU-node selector/anti-affinity, no static passwords, and must not alter Loki. Grafana is admin-only and consumes the same Prometheus service used by the API job-detail metrics endpoint.

- [ ] **Step 4: Verify chart render, DCGM scrape target, and a GPU metric query.**

Run: `bash ops/observability/prometheus/verify.sh`

Expected: `DCGM_FI_DEV_GPU_UTIL` returns at least one time series and the backend `PROMETHEUS_URL` points to the installed service.

### Task 3: Make code, images, debugging, and UI workflow production-friendly

**Files:**
- Modify: `frontend/src/views/DataCache/index.vue`
- Modify: `frontend/src/views/Job/CreateJob.vue`
- Modify: `frontend/src/views/Devcenter/index.vue`
- Modify: `frontend/src/components/DataSpacePicker.vue`
- Modify: `frontend/src/api/dataSpaces.js`
- Modify: `frontend/src/dataSpaceActions.js`
- Modify: `backend/api/workspace_snapshots.go`
- Modify: `backend/api/source_artifacts.go`
- Modify: `backend/k8s/raycluster.go`
- Modify: `images/workspace/Dockerfile`
- Create: `frontend/src/views/DataCache/index.test.js`
- Create: `frontend/src/views/Job/CreateJob.test.js`

- [ ] **Step 1: Write failing UI tests for the guided flow.** Tests must require the data page to explain `workspace -> immutable snapshot -> training wizard`, require the job wizard to offer `1 GPU`, `single-node 8 GPU`, and `two-node 16 GPU` profiles, and require all descriptions to say results are browsed in My runs without offering download.

- [ ] **Step 2: Run the UI tests and confirm RED.**

Run: `cd frontend && npm test -- --run src/views/DataCache/index.test.js src/views/Job/CreateJob.test.js`

- [ ] **Step 3: Implement the guided data/code flow.** Replace raw storage concepts with logical space labels. The workspace action publishes an immutable snapshot; Job creation consumes that snapshot. The UI must present the external `rayctl submit` route as a separate, copyable workflow and never ask for bucket endpoints or credentials.

- [ ] **Step 4: Add persistent Python dependency guidance and image constraints.** The workspace image must provide `python`, `venv`, `pip`, `git`, `curl`, `less`, and a small editor such as `nano`, run as the unprivileged Ray user, and set only user-writable Python/cache locations. The UI states that `pip` in `/workspace/.venv` persists while `apt` is intentionally unavailable and requires an approved Harbor image.

- [ ] **Step 5: Run frontend and focused backend tests.**

Run: `cd frontend && npm test -- --run`

Run: `cd backend && go test ./api ./k8s ./rayapi -count=1`

### Task 4: Implement governed personal IDC-to-TOS migration and data distribution

**Files:**
- Create: `backend/domain/data_transfer.go`
- Create: `backend/domain/data_transfer_test.go`
- Create: `backend/db/migrations/0010_data_transfers.up.sql`
- Create: `backend/repositories/data_transfers.go`
- Create: `backend/repositories/data_transfers_test.go`
- Create: `backend/api/data_transfers.go`
- Create: `backend/api/data_transfers_test.go`
- Create: `backend/k8s/data_transfer_job.go`
- Create: `backend/k8s/data_transfer_job_test.go`
- Create: `images/data-mover/Dockerfile`
- Create: `images/data-mover/move.py`
- Create: `helm/ray-train-platform/templates/idc-personal-nfs.yaml`
- Modify: `backend/config/config.go`
- Modify: `backend/api/data_spaces.go`
- Modify: `backend/main.go`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `frontend/src/views/DataCache/index.vue`
- Create: `ops/storage/idc-personal/verify.sh`

- [ ] **Step 1: Write domain tests for trusted NFS path derivation.**

```go
func TestPersonalIDCSourceRejectsTraversalAndKeepsSubjectScope(t *testing.T) {
    root := PersonalIDCSource{Server: "nfs.internal", ExportPath: "/exports/users", UserPathTemplate: "users/{subject}"}
    source, err := root.Resolve("tenant-a", "alice", "projects/run-1")
    if err != nil || source.Path != "/exports/users/users/alice/projects/run-1" { t.Fatalf("unexpected source: %#v %v", source, err) }
    if _, err := root.Resolve("tenant-a", "alice", "../../other-user"); err == nil { t.Fatal("traversal accepted") }
}
```

- [ ] **Step 2: Run the test and confirm RED.**

Run: `cd backend && go test ./domain -run TestPersonalIDCSourceRejectsTraversalAndKeepsSubjectScope -count=1`

- [ ] **Step 3: Implement the transfer state machine.** States are `queued`, `running`, `succeeded`, `failed`, `cancelled`. A request accepts a personal IDC relative source plus a writable TOS logical destination, validates caller ownership/quotas, records immutable request metadata, and creates a TTL-cleaned Job. A user cannot choose a server, export path, another subject, a raw bucket key, or a shared/public destination.

- [ ] **Step 4: Render a transfer Job with scoped mounts only.** Its NFS volume uses `subPath` equal to the server-derived personal path; its TOS volume is the caller's existing data binding. It runs non-root with no service-account cloud token and has CPU-node selector, resource limits, `activeDeadlineSeconds`, and `ttlSecondsAfterFinished`.

- [ ] **Step 5: Add the data-distribution API/UI.** The response joins logical source/destination, recursive transfer totals, status/error, and cache state. The UI shows only the caller's personal IDC subtree when enabled and has no download action.

- [ ] **Step 6: Add a Helm guard for NFS activation.** When `idcPersonalNFS.enabled=true`, require bare server, non-root clean export, and `userPathTemplate` containing exactly `{subject}`. Default is disabled. Deployment remains render-testable without the operator values.

- [ ] **Step 7: Run package tests and render verification.**

Run: `cd backend && go test ./domain ./repositories ./api ./k8s -count=1`

Run: `bash ops/storage/idc-personal/verify.sh --render-only`

### Task 5: Add safe image extension and local-NVMe cache integration

**Files:**
- Create: `backend/domain/image_build.go`
- Create: `backend/domain/image_build_test.go`
- Create: `backend/k8s/image_build_job.go`
- Create: `backend/k8s/image_build_job_test.go`
- Create: `backend/api/image_builds.go`
- Create: `backend/api/image_builds_test.go`
- Create: `helm/ray-train-platform/templates/image-builder-rbac.yaml`
- Create: `ops/storage/local-cache/verify.sh`
- Modify: `backend/config/config.go`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `frontend/src/views/QuotaManage/index.vue`
- Modify: `frontend/src/views/Devcenter/index.vue`
- Modify: `backend/k8s/rayjob.go`

- [ ] **Step 1: Write failing tests for image-build request validation.** The request may select only an owned immutable workspace snapshot and a Dockerfile path within it; it may not supply registry credentials, arbitrary push destinations, build arguments containing secrets, or an unpinned base image.

- [ ] **Step 2: Implement a disabled-by-default CPU build path.** The build Job uses a configured Harbor robot Secret and writes only to `harbor.wellspiking.ai/guofeng.su/tenants/<tenant>/<subject>/...@sha256`. The API discovers its digest, scans/publishes it to the catalog only after success, and emits an audit event. The feature returns a clear administrator-action message until a robot Secret is configured.

- [ ] **Step 3: Integrate local CSI only after the StorageClass is verified.** Enable `training.localCache` only with a verified `ray-cache-local` class. Each Ray Pod gets a generic ephemeral RWO PVC at `/mnt/cache`; no input/output logical root is redirected there. Update cache controls/UI to say “accelerated cache”, not durable storage.

- [ ] **Step 4: Verify renderer and safety tests.**

Run: `cd backend && go test ./domain ./api ./k8s -count=1`

Run: `bash ops/storage/local-cache/verify.sh`

### Task 6: Build, deploy, and conduct production acceptance

**Files:**
- Create: `examples/production/distributed_smoke.py`
- Create: `ops/platform/production-preflight.sh`
- Create: `ops/platform/e2e-production.sh`
- Modify: `build-image.sh`
- Modify: `ops/platform/deploy.sh`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/ADMIN_GUIDE.md`
- Modify: `docs/ARCHITECTURE.md`

- [ ] **Step 1: Write failing deployment guard tests.** `production-preflight.sh` must reject an unset image tag, any unpinned platform image, a Kueue quota other than 16 for the production pool, missing DCGM exporter on either production node, or a missing verified local cache class when cache is enabled.

- [ ] **Step 2: Implement build/deploy scripts.** Build and push all project images to `harbor.wellspiking.ai/guofeng.su`, record image digests, render before applying, use `helm upgrade --install --atomic --wait`, and run post-deploy API/Pod/PDB checks. Existing RayJobs must not be deleted by a platform upgrade.

- [ ] **Step 3: Validate user journeys.**

```bash
bash ops/platform/production-preflight.sh
bash ops/platform/e2e-production.sh --shape 1x1
bash ops/platform/e2e-production.sh --shape 1x8
bash ops/platform/e2e-production.sh --shape 2x8
```

Expected: each job is admitted by Kueue, has its expected worker shape, writes a unique My runs result, returns structured status/logs/metrics, and the 2x8 job schedules only on the two production nodes.

- [ ] **Step 4: Conduct security review before deployment.** Check no secret appears in diffs/images/logs, verify all new path APIs reject traversal/cross-user access, verify no new download route exists, and verify the data mover/image builder use least-privilege service accounts.

- [ ] **Step 5: Update final architecture and handoff material.** Documentation must include the production architecture image, operational topology, node-addition checklist, recovery/rollback instructions, data lifecycle/retention, and the exact user routes for Portal, UI snapshot, external `rayctl`, and custom image requests.

## Plan self-review

- Coverage: production GPU isolation (Task 1), observability (Task 2), UX/debug/TOS code/external submission (Task 3), personal IDC migration/data distribution (Task 4), custom image/NVMe cache (Task 5), and build/deploy/end-to-end security validation (Task 6) each map directly to the approved design.
- Safety: every client path is logical and relative; privileged NFS and Harbor secrets are configuration-only; tasks require targeted authorization tests.
- Scope: the only activation dependency outside the repository is the personal IDC NFS server/export/template and, optionally, a Harbor robot secret. All other work can proceed immediately.
