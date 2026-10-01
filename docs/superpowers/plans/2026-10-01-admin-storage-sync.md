# Admin Storage Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver SuperAdmin-only IDC→TOS and TOS→TOS transfers in the new Portal, including governed paths, preview, full/incremental copy, durable progress, pause/resume/cancel, schedules and real acceptance under guofeng.su's personal storage.

**Architecture:** Add an independent storage-sync control-plane module without changing existing IDC immutable dataset ingestion. PostgreSQL persists plans and run attempts; a leased controller starts bounded CPU Jobs with durable manifests/checkpoints. API resolves logical roots and enforces current membership; readonly planning precedes any writable transfer and static credentials never permit failover from an unconfirmed writer.

**Tech Stack:** Go 1.25 / Gin / GORM / PostgreSQL, Kubernetes Jobs, Python TOS SDK, new Portal Vue 3 / Element Plus.

---

## Baseline and working boundaries

- Approved user request on 2026-10-01: implement the saved design, then test in guofeng.su's own personal directory. Use a newly created acceptance subdirectory; never overwrite existing personal files or touch running training.
- Backend baseline `50ea61191472e31d1c3c7819d54cae1f644a6e68`, isolated worktree `/Users/ashersu/.codex/worktrees/admin-storage-sync/ray-train-platform`, branch `codex/admin-storage-sync`.
- Original checkout has parallel help-document and earlier handoff edits. Do not stage, revert or import unrelated changes.
- Design: `docs/superpowers/specs/2026-09-30-admin-data-sync-design.md`, now approved for implementation by the current request.
- New Portal must be based on current remote `dev`, separate from backend `frontend/`.
- Local operations: editing, source review and `git diff --check` only. All executable tests, formatting, dependency installs and builds run on `root@14.103.49.106` in an isolated detached worktree via Git bundle.
- Every implementer first writes tests, reports the RED snapshot for builder execution, then implements. Root coordinates bundles and test evidence, reviews scope then code quality, and integrates contracts. Independent owners may work concurrently without overlapping files.

## Cross-component contract

Use `backend/storagesync` for domain types and manager interfaces; repository implementation remains in `backend/repositories/storage_sync*.go`. Control routes use `/api/v1/admin/storage-sync`; run workers use a separate `/api/v1/internal/storage-sync` run/attempt-bound credential. Public JSON is camelCase and never includes NFS endpoints, secrets, bucket roots or service-account credentials.

```json
{
  "name": "jinke dataset import",
  "mode": "INCREMENTAL",
  "conflictPolicy": "UPDATE",
  "verification": "METADATA",
  "mappings": [{
    "source": {"spaceId": "idc-spk-hybrid", "relativePath": "extract/example/nusc"},
    "destination": {"spaceId": "my-files", "relativePath": "storage-sync-acceptance/example"},
    "layout": "CONTENTS"
  }],
  "schedule": {"kind": "MANUAL", "timezone": "Asia/Shanghai"},
  "concurrency": 4,
  "bandwidthBytesPerSecond": 16777216
}
```

Space identifiers follow existing data-space constants, not arbitrary filesystem roots. Team space requires explicit tenantId; personal owner is the authenticated subject. Selection aliases are normalized before confinement checks. Limits and policies are validated server-side. Accepted preview is bound to creator, plan revision, manifest digest and expiry.

Worker manifest contains resolved mappings and fingerprints in a private checkpoint workspace. Coordinator/controller resolves source configuration, worker rechecks confinement. Worker result protocol includes monotonic sequence, phase, discovered/planned/copied/verified files and bytes, skipped/extra/error counts, failure code, and final manifest digest. Successful process exit is not sufficient for run success.

## Task 1: domain, persistence and orchestration

**Owner:** backend-core worker. **Files:** `backend/storagesync/*.go`, `backend/repositories/storage_sync*.go`, `backend/db/migrations/0058_storage_sync.up.sql`, migration version assertions in `backend/db/postgres*_test.go`.

- [ ] Write domain/manager tests for path traversal, overlapping mapped prefixes, fixed run revisions, preview expiry, stable schedule slots, pause/resume transitions and stale attempt callbacks.
- [ ] Write PostgreSQL tests for concurrent run creation, active-plan occupancy, hierarchical source read/target write locks, monotonic progress, and transaction rollback after conflict. Use a disposable PostgreSQL database; SQLite does not satisfy this gate.
- [ ] Send tests-only snapshot to builder and record expected missing-feature failures before implementation.
- [ ] Implement focused models and validation, repository with explicit transactions/CAS, manager and schedule controller. All source/target paths come from resolved immutable snapshots. Never release locks on heartbeat expiry alone.
- [ ] Publish exact exported types/interfaces and HTTP/worker JSON contract to the API, runtime and UI owners before dependent implementation.
- [ ] Run scoped Go and PostgreSQL tests; confirm added-module coverage >=80%; root performs spec then code review.

```go
// Required observable behavior; exact fixture helpers belong in this task.
// Given a preview from revision 1, editing the plan to revision 2 must make
// StartRun return ErrPreviewInvalid and JobClient.Ensure calls stay at zero.
// Two concurrent requests with the same idempotency key return one run ID.
// A source read lock on a/b and target write lock on a must conflict.
```

## Task 2: Python transfer engine and worker

**Owner:** worker-engine implementer. **Files:** `images/storage-sync/Dockerfile`, `images/storage-sync/requirements.txt`, `images/storage-sync/storage_sync/*.py`, `images/storage-sync/tests/*.py`.

- [ ] Add tests for JSON/manifest mapping, source mutation, target-only JSON preservation, progress de-duplication, empty files, >1000 keys, and simulated interruption/resume.
- [ ] Execute RED tests in builder Python container before adding implementation.
- [ ] Pin SDK after official API verification. Implement separate readonly scan/preview, conditional transfer, verification and browse actions; use structured SDK progress/checkpoints rather than parsing tosutil terminal output.
- [ ] Preserve only this run's checkpoint/multipart ownership; pause retains resumable state, cancel never deletes completed destination objects. Revalidate source identity and target versions on resume.
- [ ] Test metadata-based increment and full-content verification separately, including same-size/backdated source changes and multipart ETag handling. Conditional destination semantics must be tested on both single-object and multipart completion paths; unavailable guarantees fail closed.
- [ ] Add transport tests for run/attempt token, heartbeats, monotonic counters, reconnect/failure result delivery and no secret output.
- [ ] Builder tests and image build; spec then code review.

```python
def test_target_only_json_survives_increment(sync_fixture):
    f = sync_fixture(source={"sample.bin": b"new"},
                     target={"maps/expansion/cnwxijk.json": b"keep"})
    f.run(mode="INCREMENTAL")
    assert f.target["maps/expansion/cnwxijk.json"] == b"keep"
    assert f.progress.extra_files == 1
```

## Task 3: Kubernetes/runtime and delivery configuration

**Owner:** runtime worker. **Files:** `backend/k8s/storage_sync*.go`, `backend/config/storage_sync*.go`, `helm/ray-train-platform/templates/storage-sync*.yaml`, relevant values/backend env templates, `build-image.sh` storage-sync target.

- [ ] Tests first: readonly NFS/rootfs, CPU limits and no GPU, explicit CPU selector, automountServiceAccountToken=false, persistent checkpoint volume, attempt-specific deterministic names and owner validation.
- [ ] Verify RED on builder before renderer implementation.
- [ ] Implement create/observe/stop with UID ownership, graceful drain and worker termination evidence. A deleted API Pod or unknown node cannot be interpreted as a fenced old process. Backoff must not silently start competing attempts.
- [ ] Add disabled-by-default feature config and distinct scan/transfer credential capabilities; fail closed when required config is missing. Keep current idcSync settings and pipeline intact.
- [ ] Add scoped RBAC, service account, work PVC options, workload resource/bandwidth limits, explicit `storage-sync` build target. Helm upgrade remains reuse-values plus minimal new settings.
- [ ] Builder Go config/renderer tests and Helm rendering; root reviews full generated diff before any deployment.

## Task 4: API authorization, storage resolution and application wiring

**Owner:** root. **Files:** `backend/api/storage_sync*.go`, `backend/objectstore/storage_sync*.go` if needed, minimal `backend/api/jobs.go` and `backend/main.go` wiring, `backend/storage_sync_runtime.go`.

- [ ] Write API tests for unauthenticated, PAT, Engineer, TenantAdmin denial across catalog/browse/preview/plans/runs/control/files; only a current interactive SuperAdmin succeeds.
- [ ] Add resolver fixtures: local actor targeting yolo resolves only yolo shared root; omitted team/other personal owner cannot fallback; migrated actor uses stable own storage home.
- [ ] Builder RED, then implement validated request DTOs, bounded pagination, idempotency/revision checks and sanitized public responses.
- [ ] Implement worker callbacks/readonly planning access with attempt-scoped authorization. Bind every storage operation to the work snapshot and enforce read/write phase gates.
- [ ] Integrate controller under its own Lease and current identity authorization callback. Disabling a creator halts future schedule admission and triggers controlled stop of active work.
- [ ] Complete frontend-facing contract and test API callbacks, stale attempts and credentials without exposing secret content.

## Task 5: new Portal user flow

**Owner:** Portal worker. **Files in verified new Portal checkout:** `src/views/rayTrain/api/storageSync.js`, `components/admin/StorageSyncPanel.vue`, smaller storage-sync subcomponents/helpers, `QuotaManage/index.vue`, relevant contract tests.

- [ ] Write tests for API method/path serialization, SuperAdmin visibility, mapping layout, preview acknowledgment, empty/unknown progress and stale heartbeats.
- [ ] Builder RED before implementation.
- [ ] Build source/destination selection and readonly browser/path input, per-mapping final user path preview, plan editor and schedule settings, run list/details and pause/resume/cancel/retry.
- [ ] Keep existing IDCDataSyncPanel dataset-publication functionality. Read roles from RayTrain session; never rely on Portal menu presence for authorization.
- [ ] Explain full/incremental, target extras retention and partial-copy cancellation in ordinary product language. Do not expose credentials, bucket internals or Kubernetes options.
- [ ] Verify contract tests, Dockerfile.lint and build on builder; no local pnpm install/build.

## Task 6: integrated validation and delivery

**Owner:** root with separate spec and quality/security reviewers. **Files:** `docs/STORAGE_SYNC_RUNBOOK.md`, dated validation evidence and handoff links; only after verified implementation.

- [ ] Capture committed candidate bundles and test logs from isolated builder worktree. Run gofmt check, go vet, full Go suite, real PostgreSQL migrations fresh/repeat/upgrade, Python suite and Portal gates. Resolve failures before publication.
- [ ] Verify current guofeng.su session/identity/storage-home via normal authority; never impersonate a principal or read credentials into conversation. Use a new personal `files/storage-sync-acceptance-20261001-<suffix>/` subtree and record exact user-visible path.
- [ ] Execute small IDC→personal TOS copy, TOS→TOS copy in own subtree, full/incremental/zero-change, changed same-size file, target-only JSON retention, pause/resume, cancellation and schedule scenarios. Source IDC is read only; choose a small existing non-sensitive input or explicitly scoped fixture, not the prior 270GB tree.
- [ ] Compare bytes/checksum for copied samples, progress counts and final receipts; keep test history and only clean objects explicitly created by this acceptance after results are retained.
- [ ] If deployment is part of the final validation path, first produce tested candidate, schema backup, image digests and server-side Helm diff; follow current authorization and any exact auto-review requirement. New Portal dev publication follows its CI/CD, not backend Helm.
- [ ] Update design with verified SDK/credential behavior, runbook, actual acceptance outcome and source/component versions. No skipped tests or mock-only evidence may be described as production acceptance.

## Builder command contract

### 2026-10-01 validation checkpoint (not a release record)

- Backend candidate 4 `16f6b9f7`: Python 63 tests passed. Scoped Go/config/runtime/objectstore/core and real PostgreSQL repository/db tests passed except the newly added expected RED API personal-record isolation case; that API fix is in candidate 5. Core coverage was 68.1%, below the release target.
- Latest local backend candidate is `e19aae30b206c708c8cba6b1bdd2fa77be50fb21`; its bundle is `/private/tmp/rtp-storage-sync-candidate5.bundle`. Pending targeted RED cases cover canonical TOS identity, content verification despite matching CRC, FIFO, long-preview leases, fresh paused-checkpoint retention, transactional personal ownership, readonly Job deadlines and the global bandwidth minimum.
- Latest observed Portal candidate is `c43f46a3` in `/private/tmp/raytrain-storage-sync-portal-20261001`. Full lint/build/browser checks and current remote `dev` comparison remain pending; SSH to the GitLab endpoint timed out. Do not claim remote baseline parity.
- Automatic approval review rejected the candidate 5 backend/Worker upload, as it previously rejected the full Portal archive. A combined explicit source-destination authorization question is pending for `root@14.103.49.106` and `/tmp/raytrain-storage-sync-*`. Do not retry through another agent or transfer mechanism until the user answers.
- guofeng.su's stable personal storage root was checked read-only against current user and binding records. Real acceptance has not started. Use a fresh UUID child below that user's `files/`, never a username-derived or other user's root.
- No feature has been pushed, deployed or enabled, and no existing training/storage object was changed. Runbook draft: `docs/STORAGE_SYNC_RUNBOOK.md`.

Run commands only inside `/tmp/raytrain-storage-sync-verify-20261001-*` detached worktrees, with the repository mounted read-only for tests and project-pinned Go builder/PATH/GOPROXY from release skill:

```sh
go test -count=1 ./storagesync ./repositories ./api ./k8s ./config ./objectstore
go test -count=1 -coverprofile=/tmp/storage-sync.cover ./storagesync
go vet ./...
go test -timeout=20m ./...
python3 -m unittest discover -s images/storage-sync/tests -v
docker build -f docker/Dockerfile.lint . # independent Portal candidate
```

PostgreSQL integration uses the repository's actual environment-variable contract and a disposable database discovered before execution; command/env names must be copied from current integration tests, never guessed. A skipped integration is a failed release gate. Test-only expected failures are retained as RED evidence before implementation.
