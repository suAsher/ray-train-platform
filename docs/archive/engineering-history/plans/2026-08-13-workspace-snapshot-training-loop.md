# Workspace Snapshot Training Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an engineer select a directory in their personal workspace, create an immutable code snapshot, and submit a reproducible Ray training job that reads governed data and writes results without handling TOS credentials.

**Architecture:** A narrow backend snapshot service copies only the authenticated user's workspace prefix into that user's snapshot prefix and stores immutable metadata. Submission resolves a snapshot owned by the principal and Ray's source materializer copies it from the already-authorized personal data PVC; no TOS credential is added to a Ray Pod. The Portal exposes this as a guided flow, rather than a manually typed snapshot ID.

**Tech Stack:** Go, Gin, GORM/PostgreSQL, Volcengine TOS SDK, Kubernetes/KubeRay, Vue 3, Element Plus, Node test runner.

---

### Task 1: Persist and authorize immutable workspace snapshots

**Files:**
- Create: `backend/domain/workspace_snapshot.go`
- Create: `backend/repositories/workspace_snapshots.go`
- Create: `backend/db/migrations/0009_workspace_snapshots.up.sql`
- Create: `backend/api/workspace_snapshots.go`
- Test: `backend/domain/workspace_snapshot_test.go`
- Test: `backend/api/workspace_snapshots_test.go`

- [ ] Define a snapshot with an opaque ID, tenant, owner, source-relative path, immutable TOS root, file count and creation time. Reject path traversal, empty paths, and non-owner access.
- [ ] Add a unique `(tenant_id, id)` snapshot table; never store bucket credentials or raw client paths.
- [ ] Add authenticated list/create endpoints under `/api/v1/workspace-snapshots`; creation derives every TOS prefix from the principal.
- [ ] Test creation, directory traversal rejection, and cross-user list/get isolation.

### Task 2: Copy a workspace directory into an immutable TOS prefix

**Files:**
- Modify: `backend/objectstore/store.go`
- Modify: `backend/objectstore/tos.go`
- Modify: `backend/objectstore/tos_store.go`
- Test: `backend/objectstore/tos_workspace_snapshot_test.go`

- [ ] Add an internal `WorkspaceSnapshotStore` capability accepting only server-derived source and destination roots.
- [ ] Enumerate source objects recursively, copy every object into the snapshot prefix using the TOS SDK, and create a marker for an empty directory.
- [ ] Refuse an existing snapshot target, normalize source paths, and map storage failures to non-sensitive errors.
- [ ] Test exact object-key rewriting, empty-directory snapshots, and failure behavior without exposing object-store implementation details.

### Task 3: Resolve snapshots during submission and materialize them from the personal PVC

**Files:**
- Modify: `backend/api/submission_service.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/domain/training_job.go`
- Test: `backend/api/submission_service_test.go`
- Test: `backend/k8s/rayjob_test.go`

- [ ] Make workspace source submission require an owned, READY snapshot and a ready personal data binding.
- [ ] Add the resolved personal snapshot mount only to the source init container, mounted read-only; copy snapshot contents into each Pod's `/workspace` emptyDir.
- [ ] Keep `/workspace` immutable for the submitted RayJob and retain `/mnt/storage/me` as the user's writable personal root.
- [ ] Test that forged snapshot IDs, foreign snapshots, and missing personal mount bindings are rejected before a RayJob is created.

### Task 4: Replace manually typed snapshot IDs with a guided Portal flow

**Files:**
- Create: `frontend/src/api/workspaceSnapshots.js`
- Create: `frontend/src/api/workspaceSnapshots.test.js`
- Modify: `frontend/src/views/DataCache/index.vue`
- Modify: `frontend/src/views/Devcenter/index.vue`
- Modify: `frontend/src/views/Job/CreateJob.vue`
- Modify: `frontend/src/submission.js`
- Test: `frontend/src/submission.test.js`

- [ ] Let a user create a named immutable code version from the current directory in “我的工作区”.
- [ ] Render selectable snapshot versions in the training wizard and remove the free-text snapshot ID as the normal path.
- [ ] Add clear callouts for image catalog enrollment and personal Git credential setup, with links to the appropriate page.
- [ ] Explain stable runtime paths: `/workspace`, `/mnt/data/input`, `/mnt/data/output`, and personal data roots.

### Task 5: Make storage activation an explicit production gate

**Files:**
- Modify: `ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh`
- Modify: `deploy/profiles/vke-test.yaml`
- Modify: `docs/DEPLOY_FROM_SCRATCH.md`
- Modify: `docs/USER_GUIDE.md`

- [ ] Report the exact CSI authentication condition before creating test PV/PVCs.
- [ ] Verify two isolated TOS prefixes, non-root write access, and no TOS credentials in Ray Pod specs.
- [ ] Make the deployment profile refuse `dataSpaces.enabled=true` until the verification succeeds.
- [ ] Document that FSX component IRSA is required for production and that static key authentication is not a substitute for the production acceptance gate.

### Task 6: Build, deploy, and verify the one-GPU acceptance path

**Files:**
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] Run focused backend and frontend tests, then full build validation.
- [ ] Build immutable backend/frontend/source-materializer images and deploy the test profile.
- [ ] Register the standard workspace/training images, create an isolated test user, upload code and data, create a snapshot, launch a GPU workspace, submit a one-GPU RayJob, and confirm result artifacts.
- [ ] Record any infrastructure gate that prevents the final mount-dependent run; do not claim completion without the mounted-path checks.
