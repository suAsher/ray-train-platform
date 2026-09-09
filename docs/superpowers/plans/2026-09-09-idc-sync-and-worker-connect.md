# IDC Sync and Worker Connect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace manual IDC `tosutil` copying with an auditable, immutable-input SyncRun pipeline and add owner-scoped `spk-rayjob connect` access to opted-in live training workers.

**Architecture:** A backend reconciler creates CPU-only SyncJobs that mount one deployment-owned, read-only NFS source and run `tosutil sync` into a connector-scoped transport mirror. The worker records a canonical inventory, promotes only verified changes into content-addressed immutable raw blobs, and writes a receipt; dataset publication then requires a successful SyncRun and propagates its provenance into immutable DatasetVersion and TrainingJob records. Worker connection is a separate, ticketed WebSocket/exec bridge; neither feature exposes SSH, NodePort, arbitrary pods, or user-supplied Kubernetes commands.

**Tech Stack:** Go 1.25, Gin, GORM/PostgreSQL migrations, client-go batch/core/remotecommand APIs, Kubernetes Jobs, `tosutil`, Python 3.11 worker image, Helm, Go `testing`.

---

## File map

- Create `backend/domain/idc_sync.go`: immutable connector, SyncRun, inventory entry, receipt, state-machine and validation types.
- Create `backend/repositories/idc_sync.go`: GORM records and fenced/idempotent persistence.
- Create `backend/idcsync/{inventory.go,object_store.go,controller.go,manager.go}`: canonical inventory, raw-object promotion and leader reconciliation.
- Create `backend/k8s/idc_sync_job.go`: ownership-checked CPU-only SyncJob renderer/client.
- Create `backend/cmd/idc-sync/main.go` and `images/idc-sync/{Dockerfile,requirements.txt,raytrain_idc_sync.py}`: the in-Job `tosutil` orchestration and receipt writer.
- Modify `backend/domain/dataset.go`, `backend/domain/training_job.go`, `backend/repositories/{datasets.go,jobs.go}`, `backend/api/{datasets.go,submission_service.go,jobs.go}`, `backend/datasetpublisher/{manager.go,controller.go}` and the Python publisher to bind a selected successful SyncRun to a publication and a training record.
- Create `backend/api/job_connect.go` and `backend/k8s/job_connect.go`; modify `backend/spkrayjob/{command.go,client.go,project.go}`: opt-in worker connection tickets and terminal relay.
- Create migration `backend/db/migrations/0039_idc_sync_and_job_connect.up.sql`; update embedded-migration assertions in `backend/db/postgres_test.go` and `backend/db/migration_targets_test.go`.
- Modify `backend/config/config.go`, `backend/main.go`, `build-image.sh`, `helm/ray-train-platform/{values.yaml,templates/*.yaml}` only for the new CPU SyncJob image, fixed source binding, service account and constrained credential reference.

### Task 1: Establish immutable IDC sync domain and database contracts

**Files:**
- Create: `backend/domain/idc_sync.go`
- Create: `backend/domain/idc_sync_test.go`
- Create: `backend/db/migrations/0039_idc_sync_and_job_connect.up.sql`
- Modify: `backend/db/postgres_test.go`
- Modify: `backend/db/migration_targets_test.go`

- [ ] **Step 1: Write failing domain tests for connector and run state transitions.**

```go
func TestSyncRunCannotBecomeSuccessfulWithoutImmutableReceipt(t *testing.T) {
    run := domain.IDCSyncRun{ID: "sync-1", ConnectorID: "idc-qpnuscene", State: domain.IDCSyncRunning}
    if _, err := run.Transition(domain.IDCSyncSucceeded); err == nil {
        t.Fatal("successful SyncRun without inventory and receipt was accepted")
    }
}

func TestSyncConnectorRejectsArbitraryNFSAndRawPrefixes(t *testing.T) {
    connector := domain.IDCSyncConnector{ID: "idc-qpnuscene", BindingID: "../../etc", SourceRelativePath: "/", RawPrefix: "other-team/raw"}
    if err := connector.Validate(); err == nil { t.Fatal("unsafe connector accepted") }
}
```

- [ ] **Step 2: Run the tests to verify RED.**

Run: `cd backend && go test ./domain -run 'TestSync(Run|Connector)' -count=1`

Expected: compilation failure because `IDCSyncRun` and `IDCSyncConnector` do not exist.

- [ ] **Step 3: Define domain types and validation.**

Implement `IDCSyncConnector`, `IDCSyncRun`, `IDCSyncInventoryEntry`, `IDCSyncReceipt`, and exact state values `PREVIEWING`, `TRANSFERRING`, `PROMOTING`, `SUCCEEDED`, `FAILED`, `CANCELED`. Require connector IDs, configured binding IDs, non-root relative paths, raw prefix `ray-train/raw/<connector>/`, SHA-256 digests and immutable receipt keys. Make `SUCCEEDED` require inventory and receipt key/digest; prevent any terminal-state mutation.

- [ ] **Step 4: Add additive migration 0039.**

Create `idc_sync_connectors`, `idc_sync_runs`, `idc_sync_inventory_entries`, and `idc_sync_object_refs` with tenant-safe foreign keys, bounded check constraints, unique `(connector_id,idempotency_key)`, one active run per connector partial index, immutable-success trigger, and indexes for active reconciliation and raw-object reference checks. Add nullable `source_sync_run_id` and `source_inventory_sha256` to `dataset_versions` and `training_jobs`; extend the dataset/training triggers so both fields are either null for legacy records or match a succeeded SyncRun and its inventory digest.

- [ ] **Step 5: Update migration tests.**

Append `39` to `TestMigrationVersionsEmbedded`; assert the new tables, partial active-run index, immutable receipt guard, nullable legacy compatibility and training/dataset provenance trigger fragments. Add a SQLite/Postgres integration test that rejects a training job whose copied inventory digest differs from its READY dataset version.

- [ ] **Step 6: Run domain and migration tests.**

Run: `cd backend && go test ./domain ./db -run 'Test(Sync|Migration|Training.*Dataset)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add backend/domain/idc_sync.go backend/domain/idc_sync_test.go backend/db
git commit -m 'feat: add immutable IDC sync contracts'
```

### Task 2: Persist fenced, idempotent SyncRuns and inventories

**Files:**
- Create: `backend/repositories/idc_sync.go`
- Create: `backend/repositories/idc_sync_test.go`
- Modify: `backend/repositories/datasets.go`
- Modify: `backend/repositories/jobs.go`

- [ ] **Step 1: Write failing repository tests.**

```go
func TestCreateIDCSyncRunReturnsSameRunForSameConnectorAndIdempotencyKey(t *testing.T) {
    first, _ := repo.CreateIDCSyncRun(ctx, connector.ID, "manual-1", "admin")
    second, _ := repo.CreateIDCSyncRun(ctx, connector.ID, "manual-1", "admin")
    if first.ID != second.ID { t.Fatalf("got two runs: %q %q", first.ID, second.ID) }
}

func TestCompleteIDCSyncRunRejectsObjectNotInInventory(t *testing.T) {
    err := repo.CompleteIDCSyncRun(ctx, run.ID, receiptReferencing("sha256-not-in-inventory"))
    if err == nil { t.Fatal("receipt with uninventoried blob accepted") }
}
```

- [ ] **Step 2: Implement records and repository methods.**

Expose only logical operations: `CreateOrGetConnector`, `CreateIDCSyncRun`, `ClaimIDCSyncRun`, `StoreIDCSyncInventory`, `CompleteIDCSyncRun`, `FailIDCSyncRun`, `GetSuccessfulSyncRun`, `ListActiveIDCSyncRuns`, and `WithIDCSyncWrite`. Persist inventory batches under a transaction; lock the run while validating a receipt and increment raw-object reference counts atomically. Never return NFS server, absolute path, credential material or arbitrary object-store errors to API callers.

- [ ] **Step 3: Extend dataset/training mapping.**

Round-trip `SourceSyncRunID` and `SourceInventorySHA256` through `DatasetVersionRecord` and `JobRecord`, preserving nulls for historical records. Add table-driven tests for legacy nulls, complete matching provenance and partial/mismatched rejection.

- [ ] **Step 4: Run repository tests.**

Run: `cd backend && go test ./repositories -run 'Test(IDCSync|.*Dataset.*Provenance)' -count=1`

Expected: PASS, including concurrent duplicate request coverage.

- [ ] **Step 5: Commit.**

```bash
git add backend/repositories backend/domain/dataset.go backend/domain/training_job.go
git commit -m 'feat: persist IDC sync inventories and provenance'
```

### Task 3: Build the `tosutil` SyncJob worker and immutable promotion path

**Files:**
- Create: `images/idc-sync/Dockerfile`
- Create: `images/idc-sync/requirements.txt`
- Create: `images/idc-sync/raytrain_idc_sync.py`
- Create: `images/idc-sync/test_raytrain_idc_sync.py`
- Modify: `build-image.sh`
- Create: `backend/idcsync/inventory.go`
- Create: `backend/idcsync/inventory_test.go`

- [ ] **Step 1: Write failing canonical-inventory tests.**

```go
func TestCanonicalInventoryDigestIsOrderIndependent(t *testing.T) {
    left := []domain.IDCSyncInventoryEntry{{RelativePath: "b", SHA256: digestB}, {RelativePath: "a", SHA256: digestA}}
    right := []domain.IDCSyncInventoryEntry{{RelativePath: "a", SHA256: digestA}, {RelativePath: "b", SHA256: digestB}}
    if idcsync.InventoryDigest(left) != idcsync.InventoryDigest(right) { t.Fatal("digest depends on listing order") }
}
```

- [ ] **Step 2: Implement inventory rules.**

Canonicalise relative paths; reject symlinks, sockets, devices, traversal and files outside the configured root. Reuse the prior digest only when `(path,size,mtime)` matches; hash new or changed files; accept a forced full-hash mode. Serialize sorted NDJSON with a schema version, hash the exact bytes, and derive the preview delta without reading or writing TOS.

- [ ] **Step 3: Implement worker transfer and promotion.**

The Python worker receives only run ID, fixed source mount, fixed raw prefix, credential-file reference and result path. It writes a redacted plan/result JSON, invokes `tosutil sync` with a persistent `/work/checkpoint` directory and no delete option, verifies object size after transfer, then performs TOS-side copy into `blobs/sha256/<prefix>/<digest>`. It writes inventory and receipt objects with create-if-absent semantics. It must fail if a file changes while being inspected, never print credentials, and never mark success itself; the backend validates the receipt.

- [ ] **Step 4: Build a pinned image target.**

Install a checksum-pinned `tosutil` binary in `images/idc-sync/Dockerfile`; run non-root UID 65532; give `/work` writable ownership only. Add `idc-sync` to `build-image.sh` target validation and `all` only after the standalone image test succeeds.

- [ ] **Step 5: Run tests and image smoke test.**

Run: `cd backend && go test ./idcsync -count=1`

Run: `docker build -f images/idc-sync/Dockerfile -t raytrain-idc-sync:test . && docker run --rm raytrain-idc-sync:test --help`

Expected: inventory tests PASS; image runs as non-root and prints no credential value.

- [ ] **Step 6: Commit.**

```bash
git add backend/idcsync images/idc-sync build-image.sh
git commit -m 'feat: add tosutil IDC sync worker'
```

### Task 4: Reconcile and render a fixed-source CPU-only SyncJob

**Files:**
- Create: `backend/k8s/idc_sync_job.go`
- Create: `backend/k8s/idc_sync_job_test.go`
- Create: `backend/idcsync/{controller.go,controller_test.go,manager.go,manager_test.go}`
- Modify: `backend/config/config.go`
- Modify: `backend/main.go`
- Modify: `helm/ray-train-platform/values.yaml`
- Create: `helm/ray-train-platform/templates/idc-sync-rbac.yaml`
- Create: `helm/ray-train-platform/templates/idc-sync-config.yaml`

- [ ] **Step 1: Write failing renderer/controller tests.**

```go
func TestRenderIDCSyncJobMountsOnlyConfiguredSourceReadOnly(t *testing.T) {
    job, err := k8s.RenderIDCSyncJob(spec)
    require.NoError(t, err)
    assertReadOnlyClaim(t, job, "idc-original-sync-ro", domain.IDCOriginalMountPath)
    assertNoGPUResources(t, job)
    assert.Equal(t, "Never", string(job.Spec.Template.Spec.RestartPolicy))
}

func TestControllerDoesNotStartSecondActiveRunForConnector(t *testing.T) { /* claim race asserts one Job */ }
```

- [ ] **Step 2: Add deployment-owned configuration.**

Define one named connector source in Helm values that selects only an approved `idcDataSpaces.sources` entry and a non-root relative directory. Do not accept NFS server/path, bucket, credential secret, source claim or TOS prefix from HTTP. Validate image by digest, CPU/memory bounds, timeout, queue, retention and full-verification interval in `config.Load`.

- [ ] **Step 3: Implement renderer and ownership checks.**

Render a Job in `ray-train-platform` namespace with deterministic name/hash labels, non-root security context, `automountServiceAccountToken: false`, fixed read-only NFS PVC, writable bounded work PVC/emptyDir, explicit CPU/memory only, low priority and a dedicated ServiceAccount. Reconcile by claiming a run lease, creating/getting only the matching Job, parsing a bounded result file, then either completing the receipt transaction or recording a generic failed state.

- [ ] **Step 4: Wire leader manager lifecycle.**

Instantiate the manager only when `IDC_SYNC_ENABLED=true`; start it alongside the dataset publication manager. Reuse the existing leader-safe poll/retry discipline. Immediate and scheduled triggers must create the same idempotent SyncRun; schedule parsing must reject anything outside the configured fixed connector policy.

- [ ] **Step 5: Run control-plane tests.**

Run: `cd backend && go test ./k8s ./idcsync ./config -run 'Test(IDCSync|Load.*Sync)' -count=1`

Expected: PASS; no GPU, arbitrary mount, mutable image or credential leak is accepted.

- [ ] **Step 6: Commit.**

```bash
git add backend/k8s backend/idcsync backend/config backend/main.go helm/ray-train-platform
git commit -m 'feat: reconcile governed IDC sync jobs'
```

### Task 5: Bind publication and training provenance to a successful SyncRun

**Files:**
- Modify: `backend/api/datasets.go`
- Modify: `backend/api/datasets_test.go`
- Modify: `backend/datasetpublisher/{manager.go,manager_test.go,controller.go,controller_test.go}`
- Modify: `backend/k8s/dataset_publication_job.go`
- Modify: `backend/k8s/dataset_publication_job_test.go`
- Modify: `images/dataset-publisher/raytrain_publisher/*`
- Modify: `backend/api/{submission_service.go,dataset_preflight_test.go}`

- [ ] **Step 1: Write failing publication-input tests.**

```go
func TestPublicationRejectsFailedOrOtherConnectorsSyncRun(t *testing.T) {
    _, err := manager.RequestDatasetPublication(ctx, domain.DatasetPublicationRequest{Dataset: dataset, SourceSyncRunID: failedRun.ID}, "admin")
    if err == nil { t.Fatal("failed SyncRun accepted as publication input") }
}

func TestDatasetPreflightCopiesSourceInventoryProvenance(t *testing.T) {
    job := submitWithReadyVersion(t, readyVersionFromSyncRun)
    if job.DatasetProvenance.SourceInventorySHA256 != readyVersionFromSyncRun.SourceInventorySHA256 { t.Fatal("inventory provenance lost") }
}
```

- [ ] **Step 2: Replace positional publication requests with a value object.**

Introduce `domain.DatasetPublicationRequest{Dataset, SourceSyncRunID, RequestedBy}`. Existing legacy DataSpace publications remain valid only with an empty SyncRun ID; SyncRun publication requires the run to be succeeded and approved for that dataset connector. Store both source fields before the Job is created.

- [ ] **Step 3: Make the publisher consume immutable raw inputs.**

Pass only the selected inventory and immutable raw blob prefix to the publisher. Add a source adapter that resolves each logical file from the inventory receipt; reject transport-mirror keys. The publisher may cache materialized inputs in its own bounded working directory, but every produced manifest must carry `source_sync_run_id` and `source_inventory_sha256` metadata.

- [ ] **Step 4: Extend training provenance.**

Add source run and inventory digest to `DatasetProvenance`; require both when a version contains them. Copy them in `SubmissionService.resolveDatasetSnapshot`; expose only safe IDs/digests in job responses and inject `PLATFORM_SOURCE_SYNC_RUN_ID`/`PLATFORM_SOURCE_INVENTORY_SHA256` into managed runtime environment.

- [ ] **Step 5: Run integration tests.**

Run: `cd backend && go test ./api ./datasetpublisher ./k8s ./repositories -run 'Test(.*SyncRun|DatasetPreflight|Publication|.*Provenance)' -count=1`

Expected: PASS; a training request cannot pin a failed/partial/mismatched SyncRun and no transport-mirror key reaches a RayJob.

- [ ] **Step 6: Commit.**

```bash
git add backend/api backend/datasetpublisher backend/k8s backend/repositories backend/domain images/dataset-publisher
git commit -m 'feat: pin datasets to immutable IDC sync runs'
```

### Task 6: Add owner-scoped `spk-rayjob connect` as a separate audited path

**Files:**
- Create: `backend/api/job_connect.go`
- Create: `backend/api/job_connect_test.go`
- Create: `backend/k8s/job_connect.go`
- Create: `backend/k8s/job_connect_test.go`
- Modify: `backend/domain/training_job.go`
- Modify: `backend/repositories/jobs.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/spkrayjob/{command.go,client.go,command_test.go,project.go}`
- Modify: `helm/ray-train-platform/templates/backend-rbac.yaml`

- [ ] **Step 1: Write failing permission and lifecycle tests.**

```go
func TestJobConnectRequiresOwnerOptInAndRunningWorker(t *testing.T) {
    response := requestConnect(t, otherUsersRunningJob)
    require.Equal(t, http.StatusForbidden, response.Code)
    response = requestConnect(t, ownersJobWithoutAllowConnect)
    require.Equal(t, http.StatusConflict, response.Code)
}

func TestJobConnectRejectsCallerSuppliedPodAndCommand(t *testing.T) {
    response := requestConnect(t, runningJob, `{"worker":"x","pod":"kube-system/x","command":"sh"}`)
    require.Equal(t, http.StatusBadRequest, response.Code)
}
```

- [ ] **Step 2: Persist explicit opt-in.**

Add `allow_connect BOOLEAN NOT NULL DEFAULT FALSE` to `training_jobs`; add `AllowConnect` to `JobSpec` and CLI project/submit flags as `--allow-connect`. The flag is immutable after submission. Add it to RayJob labels/annotations only as a control-plane signal; it must not inject SSH, keys or an extra port into user images.

- [ ] **Step 3: Implement deterministic worker selection and audit records.**

The API accepts only a validated Ray worker identifier, resolves the actual Pod from the platform-owned RayJob labels and owner references, checks owner/tenant/state/opt-in, then writes `CONNECT_OPENED` and `CONNECT_CLOSED` audit events. It uses a short-lived signed ticket bound to job, worker, owner and attempt; ticket verification occurs before Kubernetes exec setup and again on reconnect.

- [ ] **Step 4: Implement terminal transport without exposing Kubernetes credentials.**

Use the backend Kubernetes client to create a fixed interactive terminal exec request for the resolved worker and relay stdin/stdout/stderr/resize frames over the ticketed WebSocket. The public API accepts no namespace, pod name, container name or command. Enforce terminal idle timeout, byte limits and backend-side close on job terminal state. Grant only `pods/get` and `pods/exec` in the tenant namespaces to the backend ServiceAccount; do not grant the CLI a Kubernetes token.

- [ ] **Step 5: Add CLI command.**

Add `spk-rayjob connect JOB_ID --worker WORKER_ID`. It obtains the ticket using the stored platform token, switches the local TTY to raw mode, forwards resize events, restores terminal state on every error/signal and prints an explicit audit/session identifier. It must never write ticket values to config, stdout logs or error messages.

- [ ] **Step 6: Run tests.**

Run: `cd backend && go test ./api ./k8s ./spkrayjob -run 'Test(JobConnect|.*AllowConnect|.*Connect)' -count=1`

Expected: PASS; cross-tenant, stopped, disabled and arbitrary-target cases are denied.

- [ ] **Step 7: Commit.**

```bash
git add backend/api backend/k8s backend/spkrayjob backend/domain backend/repositories helm/ray-train-platform
git commit -m 'feat: add audited training worker connect'
```

### Task 7: Complete verification and backend-only production release

**Files:**
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/SUBMIT_GUIDE.md`
- Modify: `docs/OPERATIONS_GUIDE.md`
- Modify: `docs/NEW_TRAINING_CODE_GUIDE.md`

- [ ] **Step 1: Document the backend contracts without UI instructions.**

Document the fixed current connector, dry-run/trigger/status API semantics, source-deletion tombstone behavior, inventory/receipt provenance, dataset selection rules, SyncJob failure recovery, `spk-rayjob connect` opt-in and terminal safety boundaries. Do not document TOS credentials, NFS server addresses or raw object keys.

- [ ] **Step 2: Run full verification.**

Run: `cd backend && gofmt -l . && go build ./... && go test ./...`

Run: `git diff --check`

Expected: no gofmt output; all backend tests pass; no secret, raw prefix, NFS server or ticket is exposed in public responses/docs.

- [ ] **Step 3: Build only changed backend artifacts.**

On the build host, build `backend,idc-sync,dataset-publisher,spk-rayjob` only if each changed. Obtain every Harbor digest with `docker buildx imagetools inspect`; never build frontend.

- [ ] **Step 4: Dry-run and release with existing jobs protected.**

Create a minimal Helm override containing only rebuilt image digests and new SyncJob configuration. Back up Helm values; run server dry-run and manifest diff. Do not deploy while a user training job is actively being diagnosed; when deployment is authorised, use `--atomic --wait`, verify image IDs, health endpoint, migration logs and unchanged existing RayJob UID/pods.

- [ ] **Step 5: Perform non-destructive acceptance.**

Create a dry-run SyncRun against the fixed source, assert no upload/delete; run a small bounded sync fixture before the full production source; publish a fixture version and submit a CPU/no-GPU validation Job pinned to it. Exercise a permitted `spk-rayjob connect` session only on an explicitly opted-in disposable fixture Job; record its audit event. Do not alter the current `labeled` tree or any active training task.

- [ ] **Step 6: Commit documentation.**

```bash
git add docs
git commit -m 'docs: describe governed IDC sync and worker connect'
```

## Plan self-review

- Spec coverage: Tasks 1-5 implement source registration, SyncRun inventory/receipt, `tosutil` incremental transfer, tombstone safety, immutable DatasetVersion and TrainingJob provenance. Task 6 implements the separately scoped worker connection. Task 7 covers docs, builds, deployment and safe acceptance. Evaluation/model publication remain deliberately out of implementation scope but have provenance contracts in the approved design.
- Placeholder scan: no unresolved placeholder or undefined generic implementation task remains; each task names files, test intent, commands and expected outcomes.
- Type consistency: `IDCSyncConnector`, `IDCSyncRun`, `SourceSyncRunID`, `SourceInventorySHA256`, `DatasetPublicationRequest`, `AllowConnect` and `spk-rayjob connect` use the same names throughout.
