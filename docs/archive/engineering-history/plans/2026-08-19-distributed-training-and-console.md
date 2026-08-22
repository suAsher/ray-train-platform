# Distributed Training and Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make RayTrain a clear, testable product for frequent external code submission, single-node multi-GPU DDP, multi-node Ray Train/DDP, governed administrator operations, and understandable task lifecycle visibility.

**Architecture:** Keep the existing RayJob as the Kubernetes lifecycle and Kueue admission boundary. Add explicit distributed execution profiles: a single-worker `torchrun` profile for multi-GPU on one node, and a Ray Train adapter profile for multiple Ray workers across nodes. The Portal and `rayctl` submit the same immutable job specification; a versioned source archive remains the unit of frequent code iteration.

**Tech Stack:** Go/Gin/GORM, KubeRay/Kueue, Python/Ray Train/PyTorch DDP, Vue 3/Element Plus, Helm, TOS via FSX CSI/IRSA, Harbor digest images.

---

### Task 1: Verify the new public BEVFusion data contract before changing runtime code

**Files:**
- Create: `examples/distributed-demo/inspect_dataset.py`
- Create: `examples/distributed-demo/submit-inspect.sh`
- Test: `examples/distributed-demo/submit-inspect.test.sh`
- Modify: `docs/BEVFUSION_RUNBOOK.md`

- [ ] **Step 1: Write the failing submit-contract test**

```bash
grep -F '@ray.remote(num_gpus=1)' "$entrypoint"
grep -F 'PLATFORM_DATASET_PATH' "$entrypoint"
grep -F 'final_merged_nuscenes_infos_train.pkl' "$entrypoint"
```

- [ ] **Step 2: Run the test before the submit script exists**

Run: `bash examples/distributed-demo/submit-inspect.test.sh`

Expected: fail because the inspected distributed-demo entrypoint is absent.

- [ ] **Step 3: Implement a GPU-worker data inspector**

Create a Ray task with `num_gpus=1` that records the selected input tree, known pkl names, pkl top-level keys, referenced file existence samples, worker node IP and GPU name into `PLATFORM_OUTPUT_PATH/dataset-inspection.json`.

- [ ] **Step 4: Run the test and execute the short real task**

Run: `bash examples/distributed-demo/submit-inspect.test.sh`

Expected: `dataset inspector submission contract: ok`.

Submit from the build machine with the immutable default image and the user-selected public subdirectory. Preserve the platform record and output JSON; delete only the RayJob CR after it reaches a terminal state.

### Task 2: Define execution profiles and test their rendered resource topology

**Files:**
- Create: `backend/domain/execution_profile.go`
- Create: `backend/domain/execution_profile_test.go`
- Modify: `backend/domain/training_job.go`
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_test.go`
- Modify: `frontend/src/trainingProfiles.js`
- Modify: `frontend/src/trainingProfiles.test.js`

- [ ] **Step 1: Write failing profile validation tests**

```go
func TestExecutionProfileValidatesSingleNodeDDP(t *testing.T) {
    err := domain.ExecutionProfile{Mode: domain.ExecutionModeTorchrun, WorkerReplicas: 1, GPUsPerWorker: 2}.Validate()
    if err != nil { t.Fatal(err) }
}

func TestExecutionProfileRejectsMultiNodeTorchrun(t *testing.T) {
    err := domain.ExecutionProfile{Mode: domain.ExecutionModeTorchrun, WorkerReplicas: 2, GPUsPerWorker: 2}.Validate()
    if err == nil { t.Fatal("expected multi-node torchrun rejection") }
}
```

- [ ] **Step 2: Run the targeted package test**

Run: `cd backend && go test ./domain -run ExecutionProfile -count=1`

Expected: fail because execution profiles are not yet modeled.

- [ ] **Step 3: Implement the immutable profile contract**

Define `single_gpu`, `single_node_ddp`, and `ray_train_ddp`. Require one worker for `torchrun`; require one GPU per Ray Train worker in V1; persist the selected mode inside `JobSpec`; render the user entrypoint through the in-image `raytrain-launch` program rather than interpolating shell fragments in the API.

- [ ] **Step 4: Render manifest tests**

Assert that a two-GPU single-node job has one worker Pod requesting two GPUs, while a two-worker Ray Train job has two worker Pods requesting one GPU each and its driver runs the Ray Train adapter.

- [ ] **Step 5: Add Portal profile mapping tests**

Assert the UI labels are `单卡`, `单机多卡 DDP`, and `多机多卡 Ray Train`; reject an invalid two-worker `torchrun` request before network submission.

### Task 3: Build the default runtime image and launcher

**Files:**
- Create: `images/workspace/raytrain-launch.py`
- Create: `images/workspace/raytrain-ray-train.py`
- Create: `images/workspace/raytrain-launch.test.sh`
- Modify: `images/workspace/Dockerfile`
- Modify: `build-image.sh`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: Write launcher behavior tests**

```bash
python3 raytrain-launch.py --mode torchrun --workers 1 --gpus-per-worker 2 -- echo ok
test "$CAPTURED_COMMAND" = 'torchrun --nproc_per_node=2 echo ok'

python3 raytrain-launch.py --mode ray-train --workers 2 --gpus-per-worker 1 -- echo ok
grep -F 'TorchTrainer' "$CAPTURED_PLAN"
```

- [ ] **Step 2: Run the launcher test**

Run: `bash images/workspace/raytrain-launch.test.sh`

Expected: fail because no launcher exists.

- [ ] **Step 3: Implement the launcher**

`torchrun` mode invokes one process per allocated local GPU. `ray-train` mode submits an adapter to the existing Ray address, obtains rank/local-rank/world-size from Ray Train, then invokes the code entry function with the standard `PLATFORM_*` storage variables. The adapter writes a small rank topology JSON to the task output before invoking user code.

- [ ] **Step 4: Build and inspect the default image**

Build a new immutable `ray-workspace` digest. Verify it runs UID/GID 1000, includes `raytrain-launch`, `torchrun`, `ray.train`, internal apt/conda configuration and no static TOS credentials.

### Task 4: Expand debugging from a hard-coded single GPU workspace

**Files:**
- Modify: `backend/domain/dev_workspace.go`
- Modify: `backend/domain/dev_workspace_test.go`
- Modify: `backend/api/workspaces.go`
- Modify: `backend/api/workspaces_test.go`
- Modify: `backend/k8s/raycluster.go`
- Modify: `backend/k8s/raycluster_test.go`
- Modify: `frontend/src/views/Devcenter/index.vue`
- Create: `frontend/src/devWorkspaceProfiles.js`
- Create: `frontend/src/devWorkspaceProfiles.test.js`

- [ ] **Step 1: Write failing backend tests for workspace profiles**

```go
func TestRenderDevRayClusterAllowsTwoGPUWorker(t *testing.T) {
    workspace := domain.DevWorkspace{GPUCount: 2, WorkerReplicas: 1}
    _, err := k8s.RenderDevRayCluster(workspace, options)
    if err != nil { t.Fatal(err) }
}

func TestRenderDevRayClusterRejectsMultiWorkerJupyterWorkspace(t *testing.T) {
    workspace := domain.DevWorkspace{GPUCount: 1, WorkerReplicas: 2, Mode: "interactive"}
    if _, err := k8s.RenderDevRayCluster(workspace, options); err == nil { t.Fatal("expected rejection") }
}
```

- [ ] **Step 2: Implement a two-mode workspace model**

Keep `interactive` as one GPU worker with Jupyter/VS Code. Add `single_node_multi_gpu` for 2/4/8 GPUs in one GPU worker, which still provides terminal/Jupyter. Add `distributed_debug` with N worker Pods and a driver terminal; only the driver gets interactive endpoints, and all workers mount identical governed storage.

- [ ] **Step 3: Update UI and test profile selection**

The form starts with simple profile cards and exposes CPU/memory only in an advanced panel. Show a topology preview such as `1 节点 × 2 GPU` or `2 节点 × 1 GPU`; do not show Pod, PVC, bucket or node IP details.

### Task 5: Make job lifecycle and submission origin visible

**Files:**
- Modify: `frontend/src/views/Job/index.vue`
- Modify: `frontend/src/views/Job/JobDetail.vue`
- Create: `frontend/src/jobTimeline.js`
- Create: `frontend/src/jobTimeline.test.js`
- Modify: `frontend/src/jobOwner.js`
- Modify: `frontend/src/jobOwner.test.js`

- [ ] **Step 1: Write timeline formatting tests**

```js
assert.equal(formatDuration('2026-08-19T00:00:00Z', '2026-08-19T00:01:05Z'), '1 分 05 秒')
assert.equal(originLabel('ray-cli'), 'rayctl 外部提交')
assert.equal(finishedLabel(null), '运行中')
```

- [ ] **Step 2: Implement the list and detail timeline**

Show submitter display name, Portal/rayctl source badge, created, started/first observed, finished, duration and terminal reason. Default to `我提交的`; a regular member can explicitly view the tenant-visible queue only when policy permits, while a tenant administrator gets a clearly labelled team view and a super administrator gets an all-tenant filter.

- [ ] **Step 3: Test time fields against live API fixtures**

Use records containing nullable `finishedAt`, immutable created timestamps and both `portal` and `ray-cli` origins.

### Task 6: Replace the overloaded quota page with an administrator console

**Files:**
- Create: `frontend/src/views/Admin/index.vue`
- Create: `frontend/src/views/Admin/UsersPane.vue`
- Create: `frontend/src/views/Admin/ImagesPane.vue`
- Create: `frontend/src/views/Admin/DataPane.vue`
- Create: `frontend/src/views/Admin/SchedulingPane.vue`
- Create: `frontend/src/views/Admin/IntegrationsPane.vue`
- Create: `frontend/src/views/Admin/adminNav.js`
- Create: `frontend/src/views/Admin/adminNav.test.js`
- Modify: `frontend/src/router/index.js`
- Modify: `frontend/src/layout/Layout.vue`
- Modify: `frontend/src/views/QuotaManage/index.vue`

- [ ] **Step 1: Write a navigation test**

```js
assert.deepEqual(adminSections.map(section => section.id), [
  'overview', 'users', 'images', 'data', 'scheduling', 'integrations', 'nodes', 'jobs'
])
```

- [ ] **Step 2: Implement console sections using existing protected APIs**

Move existing tenant/user/password/quota/object-set/image/Git credential controls out of `QuotaManage`. Add direct storage asset management and a job queue panel. Each destructive action uses a named confirmation, never exposes Secret values, and presents server validation errors in the relevant form.

- [ ] **Step 3: Retire the old route without breaking saved links**

Redirect `/quota` to `/admin/scheduling`; the navigation only shows `/admin` to administrators.

### Task 7: Redesign the everyday training flow

**Files:**
- Modify: `frontend/src/views/Job/CreateJob.vue`
- Modify: `frontend/src/views/Job/index.vue`
- Modify: `frontend/src/views/Devcenter/index.vue`
- Modify: `frontend/src/views/DataCache/index.vue`
- Modify: `frontend/src/layout/Layout.vue`
- Modify: `frontend/src/brandIdentity.test.js`
- Create: `frontend/src/productionWorkflowV2.test.js`

- [ ] **Step 1: Write a workflow test**

```js
assert.match(createJobSource, /快速提交/)
assert.match(createJobSource, /高级配置/)
assert.match(createJobSource, /单机多卡 DDP/)
assert.match(createJobSource, /多机多卡 Ray Train/)
```

- [ ] **Step 2: Implement progressive disclosure**

The first screen asks only for code, environment, data, compute profile and command. A reviewed summary follows. Advanced configuration contains timeout, retry, checkpoint and low-level CPU/memory fields. Use a shared quiet dark palette, one primary action per card, consistent Chinese labels and compact helper text.

- [ ] **Step 3: Run Vue unit tests and production build**

Run: `cd frontend && npm test && npm run build`

Expected: all existing and new browser-contract tests pass; Vite reports a successful build.

### Task 8: Validate real 2-GPU and multi-node execution, then publish operating guides

**Files:**
- Create: `examples/distributed-demo/ddp_smoke.py`
- Create: `examples/distributed-demo/ray_train_smoke.py`
- Create: `examples/distributed-demo/submit-ddp-demo.sh`
- Create: `examples/distributed-demo/submit-ray-train-demo.sh`
- Create: `examples/distributed-demo/verify-results.sh`
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/BEVFUSION_RUNBOOK.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/architecture/raytrain-production-architecture-2026-08.png`

- [ ] **Step 1: Build a pinned runtime image and register it as the default training/debug image**

The image catalog entry must use its output digest, be labelled as the platform default, and identify the Ray/PyTorch/CUDA compatibility matrix. Do not publish a mutable tag.

- [ ] **Step 2: Run single-node 2-GPU DDP**

The demo writes `{rank, local_rank, world_size, hostname, gpu_name}` from every process to the personal output directory. Accept only exactly two ranks on one hostname and two distinct local GPU indices.

- [ ] **Step 3: Run two-worker distributed Ray Train**

Accept only exactly two ranks on two different hostnames, one GPU per worker, same input checksum and writable independent output directory. Capture Portal task status, timing, origin and logs.

- [ ] **Step 4: Exercise both submission paths**

Submit the DDP demo from the Portal using a workspace snapshot, then submit the Ray Train demo from the build machine using `rayctl submit --dir .`. Confirm both appear in the same account's task list with their correct origins.

- [ ] **Step 5: Update the three user-facing guides and architecture image**

Publish exact commands for: starting 2/8-GPU debug, external `rayctl` installation/login/submit, selecting data, reading task timeline/loss, checkpoint resume and the BEVFusion migration boundary. The architecture image has no IP addresses and explicitly shows Kueue, TOS/FSX/IRSA, IDC NFS and NVMe cache.

## Acceptance Criteria

- [ ] A user can submit frequently edited code from a local checkout without a kubeconfig or TOS credentials.
- [ ] One default digest image runs supported projects without custom image work; unsupported native/CUDA dependencies produce a clear image-build path.
- [ ] A two-GPU single-node DDP job proves ranks 0–1 on one host.
- [ ] A multi-node Ray Train job proves ranks on distinct hosts.
- [ ] The Portal shows created, observed start, completed, duration, submitter and origin for each job.
- [ ] Every existing protected administrator capability is discoverable in the administrator UI.
- [ ] Documentation and the architecture diagram match the live ClusterIP-only platform.
