# Dynamic Capacity, Team Data, and BEVFusion Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make GPU capacity and tenant limits reflect the live cluster, let team administrators publish team data through the Portal without making workload mounts writable, and deliver repeatable BEVFusion 2×8-GPU acceptance instructions for three submission paths.

**Architecture:** The Kubernetes node inventory is the authoritative physical-capacity source and updates an in-process last-known-good limit store as part of the existing Kueue reconciliation loop. Tenant GPU quota remains a database policy controlled by SuperAdmin; callers receive only `min(physical capacity, tenant quota)`. Data-space responses expose Portal write capability separately from immutable Pod mount permissions. Acceptance uses clean Git clones and code uploaded with each job; the runtime image remains an immutable dependency environment.

**Tech Stack:** Go 1.25, Gin, PostgreSQL, Kubernetes client-go, KubeRay, Kueue, Vue 3, Element Plus, Node test runner, Helm, Ray Jobs API, TOS/FSX CSI, Loki.

---

## Task 1: Make training-pool shape dynamic and last-known-good

**Files:**
- Modify: `backend/k8s/cluster_capacity.go`
- Modify: `backend/k8s/cluster_capacity_test.go`
- Modify: `backend/domain/resource_limits.go`
- Modify: `backend/domain/resource_limits_test.go`
- Modify: `backend/k8s/reconciler.go`
- Modify: `backend/k8s/reconciler_test.go`

1. Add failing tests proving capacity reports total GPUs, Ready node count and maximum GPUs on one node.
2. Add failing tests proving a valid observed capacity updates runtime limits and zero/error observations preserve the last valid limits.
3. Run `go test ./k8s ./domain` and confirm the new tests fail for the intended missing behavior.
4. Add `MaxGPUsPerNode` to `TrainingPoolCapacity` and calculate it only from Ready, non-virtual matching nodes.
5. Add a validated last-known-good capacity update operation to the resource-limit store; retain the deployment profile as startup fallback.
6. Update the reconciler to refresh runtime limits after each valid capacity observation, independently of whether the Kueue object changed.
7. Run `go test ./k8s ./domain` and confirm all tests pass.

## Task 2: Apply tenant quota to the caller-visible limits

**Files:**
- Modify: `backend/api/platform_limits.go`
- Modify: `backend/api/platform_limits_test.go`
- Inspect/modify if required: `backend/api/jobs.go`
- Inspect/modify if required: `backend/repositories/identity.go`

1. Add failing API tests for an Engineer with quota 8 on a 16-GPU fleet, a TenantAdmin with the same quota, and a SuperAdmin management response.
2. Prove normal callers cannot observe another tenant's quota or the unrestricted physical total through `/api/v1/limits`.
3. Run the focused API tests and confirm RED.
4. Resolve the authenticated tenant quota from the existing quota repository and clamp the response and execution-profile availability to the effective limit.
5. Keep submission validation layered: physical limit first, tenant quota second, Kueue admission last.
6. Run focused tests, then `go test ./api ./repositories ./domain`.

## Task 3: Separate Portal team publishing from Pod read-only mounts

**Files:**
- Modify: `backend/api/data_spaces.go`
- Modify: `backend/api/data_spaces_test.go`
- Modify: `backend/api/data_space_operations_test.go`
- Modify: `frontend/src/dataSpaceActions.js`
- Modify: `frontend/src/dataSpaceActions.test.js`
- Modify: `frontend/src/views/DataCache/index.vue`

1. Add failing backend tests: TenantAdmin receives `canWrite=true` for `team-shared`; Engineer receives false; the underlying space and Kubernetes mount remain read-only.
2. Add failing mutation tests for team upload/folder/delete authorization as supported by the object-store API; do not add download capability.
3. Add failing frontend tests for role-aware labels and actions.
4. Run focused Go and Node tests and confirm RED.
5. Add a response-only `canWrite` capability computed from the authenticated principal; never mutate the domain data-space or mount binding.
6. Render `管理员可发布 / Pod 只读` for TenantAdmin and `只读` for Engineer. Keep team space invalid as a training output.
7. Implement or expose safe object deletion only if the existing governed object-store contract supports it; otherwise document upload/overwrite/folder operations precisely and do not fake a delete button.
8. Run focused tests and verify `/mnt/storage/team` mount tests still assert `readOnly: true`.

## Task 4: Verify submitter identity and admin quota UI

**Files:**
- Modify: `backend/api/jobs_test.go`
- Modify if required: `frontend/src/views/QuotaManage/index.vue`
- Modify if required: `frontend/src/views/DevicesManagement/index.vue`
- Modify if required: `frontend/src/api/platform.js`

1. Add a regression test proving a TenantAdmin submission records the authenticated user subject and never the role name `admin`.
2. Verify only SuperAdmin can change tenant quota and the admin UI exposes physical capacity and per-team quota while team views expose only effective quota.
3. Add frontend tests for quota clamping and dynamic node additions.
4. Implement only missing behavior and run focused tests.

## Task 5: Full local verification, review, build, and deployment

**Files:**
- Modify if required: `helm/ray-train-platform/values*.yaml`
- Modify if required: deployment scripts under `deploy/` and `scripts/`

1. Run formatting and static checks.
2. Run all backend tests, frontend tests/build, Helm lint/render checks and delivery-script contract tests.
3. Review the complete diff for authorization, path traversal, secret leakage, regressions and accidental changes to user work.
4. Synchronize only the reviewed project files to the remote project directory.
5. Build immutable backend/frontend/CLI/workspace images in Harbor as required, capture digests, upgrade Helm and wait for rollout health.
6. Verify live `/healthz`, `/api/v1/limits`, team capability responses, Kueue quota, Services as ClusterIP and workload mounts as read-only.

## Task 6: Clean-clone BEVFusion three-path 2×8-GPU acceptance

**Files:**
- Modify: `examples/bevfusion/` only for reusable, secret-free platform adapters
- Modify: `scripts/e2e_external_spk_rayjob.sh`
- Modify: `scripts/e2e_native_ray_submit.sh`
- Modify: `scripts/e2e_portal_submit.sh`

1. On the acceptance host, clone `bev_3dod` and `bev_3dod_s1h` into a fresh timestamped directory using an ephemeral Git credential helper; never place the GitLab token in a URL, file under the project, process arguments, logs or documentation.
2. Apply the documented minimal path/runtime adapter directly to each clean clone and run local import/config preflight checks.
3. Clear any prior CLI session and authenticate as `guofeng.su` with a short-lived platform PAT created by that user.
4. For each branch, run sequential 2×8 smoke jobs using `spk-rayjob`, native `ray job submit --working-dir`, and Portal/API upload.
5. For every run verify: owner `guofeng.su`, automatic RayCluster lifecycle, two GPU nodes and 16 ranks, public input reads, personal output/checkpoint writes, Loki logs and Ray Dashboard access.
6. Record job IDs, timestamps, final state, runtime image digest and output/checkpoint locations without recording either token.
7. Clean only explicitly identified acceptance RayJobs/RayClusters/Pods after evidence is captured.

## Task 7: Consolidate the five final delivery documents

**Files:**
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/SUBMIT_GUIDE.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/BEVFUSION_CODE_CHANGES.md`
- Add/replace: `docs/NEW_TRAINING_CODE_GUIDE.md`
- Modify: `docs/README.md`
- Archive or remove redundant active documents only after checking inbound links.

1. Write the five documents from the verified commands and results, not from assumptions.
2. Explain platform PAT versus GitLab token, code-with-job behavior, public/team/personal data paths, debug persistence, logs, Dashboard, checkpoint/resume, parameter/loss analysis and common failures.
3. Include complete commands and complete minimal helper code; never reference platform-maintainer machine paths or copy commands from an internal build host.
4. Document both BEVFusion branches' exact changes and a generic adaptation checklist for future repositories.
5. Render the final architecture diagram from source, showing Kueue, live capacity, tenant quota, storage roots, NVMe cache, observability and three submit paths without IP addresses.
6. Run link/path/secret scans and compare every command against acceptance evidence.
