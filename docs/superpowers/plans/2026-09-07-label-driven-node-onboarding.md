# Label-driven Node Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Make mounted-disk, two-label onboarding automatic without mutating existing training.

**Architecture:** Separate controller owns runtime cache registration and a readiness label. Existing shared training selector gates new work and capacity. Dedicated fixed probes establish host disk and real PVC readiness; no SSH credentials or host package installation in the controller.

**Tech Stack:** Go/client-go, Kubernetes Pods/PVCs/ConfigMaps/Leases, Helm, existing Vue help.

## Execution order and safety

- [x] Diagnose current debug failure before configuration changes: Scheduled=True on 229, five NFS volumes fail with missing mount helper.
- [x] Install only missing `nfs-common` and dependencies on 229 after apt simulation; no upgrades or restarts. Observe kubelet retry reaching image pull.
- [x] Validate independent mounted data disks and create only missing root:root 0770 cache roots.
- [x] Append 229 to live Helm cache maps after backup/dry-run; verify actual dual-PVC non-root writes and reclaim probes.
- [x] Observe user's replacement debug reach Ready; preserve training Pod UIDs and restarts.

## Task 1: Document OS prerequisites and failure interpretation

Files: `frontend/src/help/cliOnboarding.js`, its test, `backend/helpdocs/seed.json`, `docs/OPERATIONS_GUIDE.md`.

- [x] Add failing help assertions for `nfs-common`, `command -v mount.nfs`, Scheduled/FailedMount distinction and no-restart recovery.
- [x] Implement prerequisite text without claiming automatic onboarding exists.
- [x] Regenerate embedded seed and run `node --test frontend/src/help/cliOnboarding.test.js frontend/src/help/seed.test.js frontend/src/helpContent.test.js` (24 pass).
- [ ] Review and commit exact changes; existing admin-edited DB help must not be overwritten.

## Task 2: Isolated runtime mapping ownership

Files: cache chart helpers, values, deployment, RBAC and chart contract tests.

- [ ] Add failing tests that `existingConfigMap` defaults to the legacy complete config and an explicit external name controls both volume reference and `--configmap-name`; config reload permissions follow that name.
- [ ] Implement a validated DNS-name `existingConfigMap` optional value; retain legacy Helm ConfigMap for rollback. Never generate an empty external map from static values.
- [ ] Check every ConfigMap consumer, including monitor, consistently uses the selected complete map.
- [ ] Run existing cache chart/ops contract tests and server-render default/opt-in manifests. Default output must remain unchanged.

## Task 3: Controller and probes

Files: new `backend/nodeonboarding/`, `backend/cmd/node-onboarding/`, independent Dockerfile and optional chart.

- [ ] Test pure eligibility and strict map merge first: UID changes, duplicate/default entries, wrong paths, malformed maps, preservation of all foreign mappings.
- [ ] Implement bounded leader-elected reconciliation for matching node UIDs. ConfigMap operations use resource-version concurrency protection; partial two-map success never marks ready.
- [ ] Test and implement immutable fixed prep probe: validate host mountinfo with root-disk and same-device rejection, exact paths, no symlinks, existing-root ownership/mode, no destructive operations. No host root, hostPID, privileged mode or probe SA token.
- [ ] Test and implement node-bound dual-PVC non-root smoke and configured read-only NFS smoke. Bind through node selector, not nodeName bypass of WaitForFirstConsumer. Verify PV node affinity and exact cache roots. Missing host NFS client yields actionable error, never remote package installation.
- [ ] Test failure, retry, controller restart, deletion/recreation, ownership/UID-precondition cleanup, timeout and retained mappings. Set ready only after verified probe reclamation and latest UID/labels/config recheck.
- [ ] Enforce narrow RBAC plus admission-policy Pod-template/Node-field restrictions before enabling any host-operation feature. Deploy default-disabled.

## Task 4: Shared gate and zero capacity

Files: `backend/domain/resource_limits.go`, `backend/k8s/kueue_quota.go`, observer/reconciler tests, platform Helm training selector/flavor configuration.

- [ ] Write tests distinguishing successful empty node list from API read failure. Empty list must close new admission; API failures preserve last observation and show unhealthy status.
- [ ] Implement zero eligible capacity without making unrelated per-worker configuration invalid or evicting existing workloads.
- [ ] Gate configured training selector and compatible ResourceFlavor selection on controller-owned cache-ready. Preserve all current GPU-model/pool requirements.
- [ ] Test RayJob/dev templates include gate only for new renders and current object reconciliation does not patch active templates.

## Task 5: Activation and verification

- [ ] Full Go and frontend tests/build, focused race tests, security/spec/code review.
- [ ] Commit/push and git-bundle synchronize exact source through release skill; build only changed images.
- [ ] Back up both complete cache configurations and validate no pending legacy workloads can bypass rollout gate.
- [ ] Seed complete runtime maps, install restricted controller, validate existing/229 nodes, switch configuration references, then enable selector only after successful acceptance.
- [ ] Confirm 24 GPU convergence, debug readiness, unchanged original training UIDs/restarts, and no temporary probe resources. Record exact release/digests and rollback instructions preserving new mappings.

No phase is complete merely because documentation or unit tests pass. Until Task 5 is verified, report automatic onboarding as not deployed.

## Live incident verification (2026-09-07)

- Node `172.28.1.229` was already schedulable. Original dev head/worker were
  assigned there, then failed five NFS mounts because `mount.nfs` was absent.
- Installed only `nfs-common`, `rpcbind`, `keyutils`, `libnfsidmap1`; apt simulation
  and execution showed zero upgrades/removals. No kubelet/containerd/node restart.
- Verified `/data1`=`/dev/vdb1`, `/data2`=`/dev/vdc1`, root=`/dev/vda2`; created
  exact missing cache roots root:root 0770. Both cache releases upgraded from
  revision 1 to 2, preserving 232/233 mappings. Dry-run also included the current
  unused metrics collector script; monitoring is disabled in these two releases
  and the monitor uses the separate `ray-cache-local-config` unchanged.
- New/old nodes all have `vke.volcengine.com/image-accelerate-enabled=true` and
  default overlayfs. Current dev Pod has no lazyload label; workspace manifest
  has 23 layers totaling 6,521,612,843 compressed bytes. Do not infer disabled
  node acceleration from overlayfs or advise removing/rejoining the node.
- Busybox cold pull stalled although anonymous authenticated manifest GET/HEAD
  succeeded. Preloaded the identical digest through a bounded OCI stream,
  verifying SHA256 of manifest/config/layers (2.1MiB). This was a targeted recovery,
  not a claimed diagnosis of containerd internals. No daemon restart.
- Original dev `dev-0e7629b9046444cb94be97ae` was stopped manually by user
  (explicit confirmation). Replacement `dev-c14aede904f658953f8b1f2d` became ready:
  head on 229, GPU worker on 232, both 1/1 and zero restarts. Provisioned condition
  timestamp `2026-09-07T07:55:47Z`. This is not a GPU-worker-on-229 training smoke.
- Dedicated non-root 1000 dual-PVC probe returned `DUAL_CACHE_NONROOT_OK`; both
  PV node affinities were 229 and paths under the correct data disk. After an
  initial image timeout, only the second temporary Pending PVC was recreated.
  Final PVs `pvc-f9feff13-0a63-4a1e-9b31-d7c7d5fe5175` and
  `pvc-372169e1-1ceb-4df7-a3ee-29fb01fcfd32` were reclaimed; failed-attempt UID
  `e7e686ae-419e-4036-90bb-a20fa7c4a2bf` directory was also confirmed absent.
  No test Pods/PVCs remain. Cache monitor on 229 is 2/2 Ready.
- Original training head UID `b4509e30-8f43-41bc-8632-85ec7f32648c` and worker UID
  `4471eefc-668f-4870-9c6f-77fa7e4dde8c` unchanged, both zero restarts.
- Platform release remains revision 179 at this point; new controller, UI node
  status, external cache config option and zero-capacity changes are local work,
  not production features yet.

Backups and minimal overrides retained on build host:
`/root/cache-data1-before-229-20260907.yaml`,
`/root/cache-data2-before-229-20260907.yaml`, both `*-before-229-manifest.yaml`,
`/root/ray-cache-229-data1.yaml`, `/root/ray-cache-229-data2.yaml`.
