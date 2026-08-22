# SFTP Personal Storage and NVMe Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver governed personal IDC↔TOS data migration, user-owned Harbor digest registration, node-local NVMe cache, and production acceptance evidence.

**Architecture:** The backend owns identity, authorization, request state, and Kubernetes Job rendering. A transfer Job copies only between an authenticated user's bound SFTP account and the user's existing personal FSX/TOS PVC. User images are immutable catalog entries gated by tenant-admin approval. NVMe remains disposable cache mounted through node-aware claims, never a user data root.

**Tech Stack:** Go/Gin/GORM/PostgreSQL, Kubernetes batch Jobs and PVCs, KubeRay, rclone SFTP, FSX CSI for TOS, Helm, Vue 3/Element Plus, Harbor.

---

### Task 1: Lock the personal SFTP domain contract

**Files:**
- Create: `backend/domain/idc_sftp_connection_test.go`
- Create: `backend/domain/idc_sftp_connection.go`
- Create: `backend/domain/data_transfer_test.go`
- Create: `backend/domain/data_transfer.go`

- [x] **Step 1: Write failing domain tests.** Cover an allowed `guofeng.su`
  username; reject host/path/username traversal; derive only clean relative
  paths; prohibit shared/public/run destinations; and prove API projections
  never carry private-key bytes.
- [x] **Step 2: Run `go test ./domain -run 'TestIDC|TestDataTransfer' -count=1` and observe RED.**
- [x] **Step 3: Implement immutable SFTP connection, transfer request and
  state-machine types.** Valid states are `pending`, `ready`, `queued`,
  `running`, `succeeded`, `failed`, and `cancelled`; copy direction is
  explicit and destructive sync/delete is absent.
- [x] **Step 4: Re-run the focused tests and verify GREEN.**

### Task 2: Persist bindings and transfer audit records

**Files:**
- Create: `backend/db/migrations/0010_idc_sftp_transfers.up.sql`
- Create: `backend/repositories/idc_sftp_transfers.go`
- Create: `backend/repositories/idc_sftp_transfers_test.go`
- Modify: `backend/repositories/schema_mapping_test.go`

- [x] **Step 1: Write failing repository tests for owner-only list/get,
  idempotent transfer creation, and private-key exclusion from records.**
- [x] **Step 2: Run `go test ./repositories -run 'TestIDC|TestDataTransfer' -count=1` and observe RED.**
- [x] **Step 3: Add migration and repository methods with parameterized GORM
  queries.** Store remote username, public key, Secret name, status and
  transfer summary; never store a private key or raw remote root.
- [x] **Step 4: Re-run repository tests and verify GREEN.**

### Task 3: Render least-privilege transfer Jobs

**Files:**
- Create: `backend/k8s/data_transfer_job_test.go`
- Create: `backend/k8s/data_transfer_job.go`
- Modify: `backend/k8s/client.go`
- Modify: `helm/ray-train-platform/templates/backend-rbac.yaml`
- Create: `helm/ray-train-platform/templates/data-mover-rbac.yaml`
- Create: `images/data-mover/Dockerfile`
- Create: `images/data-mover/entrypoint.sh`

- [ ] **Step 1: Write failing render tests.** Assert one personal claim, one
  read-only key Secret, one pinned known-hosts ConfigMap, no cloud env/secret,
  non-root security context, deadline/TTL, CPU-node selector, and no shell
  interpolation of user paths.
- [ ] **Step 2: Run `go test ./k8s -run TestRenderDataTransfer -count=1` and observe RED.**
- [ ] **Step 3: Implement Job rendering and rclone invocation.** Use
  `key_file` and `known_hosts_file`, `copy` only, JSON summary output, and
  `/tos/files/<clean-relative-path>` for personal input/output.
- [ ] **Step 4: Re-run renderer tests and a Helm render verification.**

### Task 4: Add connection and migration APIs

**Files:**
- Create: `backend/api/idc_sftp_transfers_test.go`
- Create: `backend/api/idc_sftp_transfers.go`
- Modify: the existing backend handler definition
- Modify: `backend/main.go`
- Modify: `backend/config/config.go`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`

- [ ] **Step 1: Write failing HTTP tests.** Require interactive authentication,
  user ownership, host/path rejection, generate-public-key response without a
  private key, and deny arbitrary SFTP targets.
- [ ] **Step 2: Run `go test ./api -run TestIDC -count=1` and observe RED.**
- [ ] **Step 3: Implement `/api/v1/idc-sftp-connection` and
  `/api/v1/data-transfers` handlers.** Generate key pairs server-side, write
  private keys only with the Kubernetes Secret client, use a fixed configured
  host and pinned known-hosts value, and enqueue one transfer Job per request.
- [ ] **Step 4: Run API tests and a production-config validation test.**

### Task 5: Make the flow understandable in the Portal

**Files:**
- Create: `frontend/src/views/DataTransfer/index.vue`
- Create: `frontend/src/views/DataTransfer/dataTransfer.test.js`
- Create: `frontend/src/api/dataTransfers.js`
- Modify: `frontend/src/router/index.js`
- Modify: the project navigation component
- Modify: `frontend/src/views/DataCache/index.vue`

- [ ] **Step 1: Write failing UI tests.** Require copyable public key, fixed
  service host wording, logical source/destination pickers, both copy
  directions, transfer status, no raw bucket/AK/SK/private-key field and no
  download action.
- [ ] **Step 2: Run the focused frontend test and observe RED.**
- [ ] **Step 3: Implement the Data Migration page and My Data entry points.**
  Use existing `DataSpacePicker` for TOS, a server-scoped IDC browser for the
  remote account, and exact user guidance for public-key setup.
- [ ] **Step 4: Run all frontend unit tests and build.**

### Task 6: Permit self-service Harbor digest requests

**Files:**
- Create: `backend/domain/image_registration_test.go`
- Create: `backend/domain/image_registration.go`
- Create: `backend/api/image_registrations_test.go`
- Create: `backend/api/image_registrations.go`
- Create: `backend/db/migrations/0011_image_registration_requests.up.sql`
- Modify: `backend/repositories/images.go`
- Modify: `backend/api/images.go`
- Modify: `frontend/src/views/AccountSecurity/index.vue`
- Modify: `frontend/src/views/QuotaManage/index.vue`

- [ ] **Step 1: Write failing tests for registry host/prefix/digest-only
  validation and admin-only approval.**
- [ ] **Step 2: Run focused domain/API tests and observe RED.**
- [ ] **Step 3: Implement pending registration records and approval that creates
  a catalogue entry.** Browser-side credentials are never accepted; image
  pulls continue via deployment-owned Kubernetes pull Secret.
- [ ] **Step 4: Verify only published immutable digest images appear in debug
  and training selectors.**

### Task 7: Provision and validate two-disk cache pools

**Files:**
- Create: `ops/storage/local-cache/prepare-node-cache.sh`
- Create: `ops/storage/local-cache/10-local-pv-cache.yaml`
- Create: `ops/storage/local-cache/verify.sh`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `backend/k8s/rayjob_cache_test.go`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/ADMIN_GUIDE.md`

- [ ] **Step 1: Write a failing render verifier.** It must require both
  `/data1/ray-cache` and `/data2/ray-cache`, `WaitForFirstConsumer`, node
  affinity, a disposable reclaim policy, and deny `hostPath` mounts in
  workloads.
- [ ] **Step 2: Run `bash ops/storage/local-cache/verify.sh --render-only` and observe RED.**
- [ ] **Step 3: Create non-destructive node preparation and static local PV
  definitions.** Split fixed cache slots across each disk; no partitioning,
  formatting, RAID, or LVM action is permitted.
- [ ] **Step 4: Enable the existing Ray ephemeral PVC renderer only after
  binding/scheduling verification on both production GPU nodes.**
- [ ] **Step 5: Run cache render, bind, 1x8 RayJob, and cleanup verification.**

### Task 8: Complete production acceptance

**Files:**
- Create: `ops/platform/e2e-personal-storage.sh`
- Create: `ops/platform/e2e-production-gpu.sh`
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/ARCHITECTURE.md`

- [ ] **Step 1: Verify SFTP host key and deploy the profile with a test user.**
- [ ] **Step 2: Run IDC→TOS and TOS→IDC copy verification with byte/file
  counts and a checksum fixture.**
- [ ] **Step 3: Validate debug data mounts, one-GPU, one-node eight-GPU, and
  two-node sixteen-GPU jobs; each must write to My runs.**
- [ ] **Step 4: Validate user separation, no download API/button, completed
  logs, GPU metrics, cache behavior, and retry/cancellation paths.**
- [ ] **Step 5: Conduct final secret/path/authorization review before enabling
  the feature for all users.**

## Plan self-review

- The plan replaces the prior personal-NFS proposal with a bounded SFTP
  transfer channel and explicitly prevents private keys from entering TOS,
  PostgreSQL, Git, Helm values, or Ray Pods.
- It uses the existing governed TOS personal PVC and existing image catalogue
  rather than introducing a second user storage model.
- Live activation is gated on an independently verified `known_hosts` entry;
  implementation and non-live render tests can proceed without it.
