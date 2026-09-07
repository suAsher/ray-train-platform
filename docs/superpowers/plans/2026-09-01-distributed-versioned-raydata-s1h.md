# 分布式版本化 Ray Data S1H Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不改变既有 `ray-ddp` 或运行中任务的前提下，发布可恢复、增量的 Parquet 版本，并让 S1H 通过 Ray Train、Ray Data 与 bounded NVMe 读取该版本。

**Architecture:** 一个发布 run 分为 plan、indexed pack、finalize 三个 CPU-only Kubernetes Job 阶段。plan 冻结可信 source inventory；indexed pack 的每个 completion 只处理一个确定分区；finalize 只接受完整且校验过的 receipts，才原子地置版本为 `READY`。训练只在用户显式选择 `streaming` + `ray-train` 时走新路径。

**Tech Stack:** Go、GORM/PostgreSQL、Kubernetes Indexed Job、Kueue、Python、PyArrow、Ray Data、Ray Train、Prometheus。

---

### Task 1: Persist partition lifecycle and leases

**Files:**
- Modify: `backend/domain/dataset.go`
- Modify: `backend/repositories/datasets.go`
- Modify: `backend/repositories/dataset_publication_runs.go`
- Create: `backend/db/migrations/0026_dataset_publication_partitions.up.sql`
- Test: `backend/domain/dataset_test.go`
- Test: `backend/repositories/dataset_publication_runs_test.go`

- [ ] **Step 1: Write the failing lifecycle tests**

```go
func TestDatasetPartitionRejectsUnsafePlanDigest(t *testing.T) {
    value := validDatasetPartition()
    value.PlanSHA256 = "not-a-digest"
    if err := value.Validate(); err == nil { t.Fatal("expected error") }
}

func TestDatasetPartitionNeedsReceiptBeforeCompletion(t *testing.T) {
    value := validDatasetPartition()
    if _, err := value.TransitionTo(DatasetPartitionCompleted, time.Now().UTC()); err == nil {
        t.Fatal("expected error")
    }
}
```

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./domain -run 'TestDatasetPartition(RejectsUnsafePlanDigest|NeedsReceiptBeforeCompletion)' -count=1`

Expected: FAIL because lifecycle properties and transition API do not exist.

- [ ] **Step 3: Implement the lifecycle and migration**

```go
type DatasetPartitionState string
const (
    DatasetPartitionPending DatasetPartitionState = "PENDING"
    DatasetPartitionLeased DatasetPartitionState = "LEASED"
    DatasetPartitionCompleted DatasetPartitionState = "COMPLETED"
    DatasetPartitionFailed DatasetPartitionState = "FAILED"
)

type DatasetPartition struct {
    ID, DatasetVersionID, Name, InputFingerprint, PlanSHA256, ReceiptSHA256 string
    State DatasetPartitionState
    Attempt int64
    LeaseOwner string
    LeaseExpiresAt *time.Time
}
```

Add non-null `state`, `input_fingerprint`, `plan_sha256`, `receipt_sha256`, `attempt`, `lease_owner`, and nullable `lease_expires_at` columns plus index `(dataset_version_id, state, lease_expires_at, name)`. Require safe digests, a receipt for completion, and an active matching lease for completion. Claim only pending/expired rows with `FOR UPDATE`; scope every action using the existing manageable dataset version query.

- [ ] **Step 4: Verify GREEN and commit**

Run: `cd backend && go test ./domain ./repositories ./db -run 'TestDatasetPartition|TestDatasetPublication|TestMigration' -count=1`

Expected: PASS.

```bash
git add backend/domain/dataset.go backend/domain/dataset_test.go backend/repositories/datasets.go backend/repositories/dataset_publication_runs.go backend/repositories/dataset_publication_runs_test.go backend/db/migrations/0026_dataset_publication_partitions.up.sql && git commit -m "feat: persist recoverable dataset publication partitions"
```

### Task 2: Publish deterministic plans, receipts, and incremental reuse

**Files:**
- Create: `images/dataset-publisher/raytrain_publisher/distributed_publish.py`
- Modify: `images/dataset-publisher/raytrain_publisher/cloud_publish.py`
- Modify: `images/dataset-publisher/raytrain_publisher/tos_storage.py`
- Test: `images/dataset-publisher/tests/test_distributed_publish.py`
- Test: `images/dataset-publisher/tests/test_cloud_publish.py`

- [ ] **Step 1: Write failing publisher tests**

```python
def test_unchanged_partition_reuses_verified_prior_receipt():
    current = build_publication_plan(trusted_index([sample("a")]), partitions=2)
    next_plan = build_publication_plan(trusted_index([sample("a"), sample("b")]), partitions=2, base_receipts=receipts_for(current))
    assert next_plan.partition_for("a").reuse_receipt is True

def test_finalize_rejects_missing_or_wrong_receipt():
    with pytest.raises(CloudPublishError):
        finalize_publication(plan=plan_with_two_partitions(), receipts=[valid_receipt(0)])
```

- [ ] **Step 2: Verify RED**

Run: `python3 -m pytest images/dataset-publisher/tests/test_distributed_publish.py -q`

Expected: FAIL because distributed functions do not exist.

- [ ] **Step 3: Implement `plan`, `pack`, and `finalize`**

```python
def partition_ordinal(token: str, partition_count: int) -> int:
    return int(hashlib.sha256(token.encode("utf-8")).hexdigest()[:16], 16) % partition_count

def partition_fingerprint(samples: Sequence[_RemoteSample]) -> str:
    rows = "\n".join(f"{s.sample['token']}\x00{s.source_key}\x00{s.source.size}\x00{s.source.sha256 or ''}" for s in samples)
    return hashlib.sha256(rows.encode("utf-8")).hexdigest()
```

`plan` verifies source metadata and writes canonical per-partition plans. It reuses only matching fingerprints with a verified base receipt. `pack --partition-ordinal N` conditionally writes `sha256-<digest>.parquet`, rechecks it, then writes a canonical receipt. `finalize` requires exactly one matching receipt for every ordinal before writing a manifest/result. Keep the single-pod `cloud_publish.py` entrypoint unchanged unless explicitly selected. Never log source keys, credentials, endpoints, or local paths.

- [ ] **Step 4: Verify GREEN and commit**

Run: `python3 -m pytest images/dataset-publisher/tests/test_distributed_publish.py images/dataset-publisher/tests/test_cloud_publish.py images/dataset-publisher/tests/test_tos_storage.py -q`

Expected: PASS, including replay, interruption, invalid receipt, and incremental reuse.

```bash
git add images/dataset-publisher/raytrain_publisher images/dataset-publisher/tests && git commit -m "feat: add resumable incremental Parquet publication"
```

### Task 3: Render opt-in bounded Indexed Jobs

**Files:**
- Modify: `backend/datasetpublisher/controller.go`
- Modify: `backend/k8s/dataset_publication_job.go`
- Modify: `backend/config/config.go`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/templates/dataset-publisher-config.yaml`
- Test: `backend/datasetpublisher/controller_test.go`
- Test: `backend/k8s/dataset_publication_job_test.go`
- Test: `backend/config/config_test.go`

- [ ] **Step 1: Write failing rendering tests**

```go
func TestDistributedPackRendersBoundedIndexedJob(t *testing.T) {
    job := mustRenderDistributedPack(t, 7803, 16)
    assertEqual(t, int64(7803), nestedInt64(t, job, "spec", "completions"))
    assertEqual(t, int64(16), nestedInt64(t, job, "spec", "parallelism"))
    assertEqual(t, "Indexed", nestedString(t, job, "spec", "completionMode"))
}

func TestDisabledDistributedPublisherRendersExistingSingleJob(t *testing.T) {
    assertAbsent(t, mustRenderLegacyPublisher(t), "spec", "completionMode")
}
```

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./datasetpublisher ./k8s ./config -run 'Test(DistributedPack|DisabledDistributedPublisher)' -count=1`

Expected: FAIL because the opt-in distributed configuration does not exist.

- [ ] **Step 3: Implement the scheduler contract**

Add `datasetPublisher.distributed.enabled`, `maxParallelism`, `partitionCount`, `partitionLeaseSeconds`, and `maxPartitionAttempts`; defaults preserve disabled behavior. When enabled, render deterministic plan/pack/finalize Jobs. Only pack uses Indexed completion and bounded parallelism. Preserve Kueue label, low priority, CPU/memory-only resources, service account, deadline, and retry settings. The controller must never alter `READY` versions, RayJobs, RayClusters, PVCs, or tenant resources.

- [ ] **Step 4: Verify GREEN and commit**

Run: `cd backend && go test ./datasetpublisher ./k8s ./config -count=1 && cd .. && helm template ray-platform helm/ray-train-platform --set datasetPublisher.enabled=true --set datasetPublisher.distributed.enabled=true >/tmp/ray-platform-rendered.yaml`

Expected: PASS and successful Helm rendering.

```bash
git add backend/datasetpublisher backend/k8s/dataset_publication_job.go backend/k8s/dataset_publication_job_test.go backend/config helm/ray-train-platform && git commit -m "feat: schedule bounded distributed dataset publishers"
```

### Task 4: Expose Ray Data, bounded NVMe, and spill metrics safely

**Files:**
- Modify: `images/workspace/raytrain_runtime/ray_data.py`
- Modify: `images/workspace/raytrain_runtime/s1h_parquet.py`
- Modify: `images/workspace/raytrain_runtime/shard_cache.py`
- Modify: `images/workspace/raytrain_runtime/managed_driver.py`
- Modify: `backend/k8s/rayjob.go`
- Test: `images/workspace/raytrain_runtime/test_s1h_parquet.py`
- Test: `images/workspace/raytrain_runtime/test_shard_cache.py`
- Test: `backend/k8s/rayjob_streaming_manifest_test.go`

- [ ] **Step 1: Write failing cache/spill tests**

```python
def test_resolver_reports_path_free_digest_scoped_cache_metrics(tmp_path):
    resolver = resolver_for(tmp_path, cache_policy="bounded")
    resolver.resolve_rows([manifest_row()])
    assert resolver.metrics_snapshot()["cache_miss"] == 1
    assert str(tmp_path) not in repr(resolver.metrics_snapshot())
```

```go
func TestStreamingRayTrainSeparatesSpillingFromShardCache(t *testing.T) {
    job := renderStreamingRayTrain(t, domain.DatasetCachePolicyBounded)
    assertEnvironmentPathPairDistinct(t, job, "RAY_object_spilling_config", "PLATFORM_CACHE_PATH")
}
```

- [ ] **Step 2: Verify RED, implement, and verify GREEN**

Run RED: `python3 -m pytest images/workspace/raytrain_runtime/test_s1h_parquet.py images/workspace/raytrain_runtime/test_shard_cache.py -q && cd backend && go test ./k8s -run TestStreamingRayTrainSeparatesSpillingFromShardCache -count=1`

Implement aggregate path-free `cache_hit`, `cache_miss`, `cache_download_bytes`, `cache_eviction`, `cache_fallback`, and checksum metrics at the S1H resolver boundary. Preserve content-addressed LRU, do not preload `streaming`, and retain spilling under `ray-spill/objects`, disjoint from cache/checkpoint/output paths.

Run GREEN: `python3 -m pytest images/workspace/raytrain_runtime/test_ray_data.py images/workspace/raytrain_runtime/test_s1h_parquet.py images/workspace/raytrain_runtime/test_shard_cache.py -q && cd backend && go test ./k8s -run 'Test.*(Streaming|Cache|Spill)' -count=1`

Expected: RED first; GREEN PASS after implementation.

```bash
git add images/workspace/raytrain_runtime backend/k8s/rayjob.go backend/k8s/rayjob_streaming_manifest_test.go && git commit -m "feat: expose bounded streaming shard cache metrics"
```

### Task 5: Add S1H three-entry controlled acceptance assets

**Files:**
- Create: `examples/bevfusion/s1h-streaming/portal-request.json`
- Create: `examples/bevfusion/s1h-streaming/.spk-rayjob.yaml`
- Create: `examples/bevfusion/s1h-streaming/native-ray-job.py`
- Create: `examples/bevfusion/s1h-streaming/preflight.py`
- Create: `examples/bevfusion/s1h-streaming/verify-results.py`
- Create: `examples/bevfusion/s1h-streaming/test_contract.py`
- Create: `docs/S1H_STREAMING_ACCEPTANCE.md`
- Modify: `docs/BEVFUSION_END_TO_END_GUIDE.md`

- [ ] **Step 1: Write failing equivalence test**

```python
def test_three_entries_pin_the_same_streaming_contract():
    templates = load_submission_templates()
    assert {item.engine for item in templates} == {"ray-train"}
    assert {item.cache_policy for item in templates} == {"bounded"}
    assert {item.dataset_selector for item in templates} == {"${READY_VERSION_ID}"}
```

- [ ] **Step 2: Verify RED**

Run: `python3 -m pytest examples/bevfusion/s1h-streaming/test_contract.py -q`

Expected: FAIL because templates do not exist.

- [ ] **Step 3: Implement non-destructive templates**

Templates select a `READY` version through the public platform API/CLI, not object-store or cluster identifiers. `preflight.py` reads a fixed small sample count; every 2×8 template uses `ray-train`, `streaming`, `bounded`, unique output, and provenance metadata. Documentation prohibits `kubectl exec`, direct Pod changes, deletion, and changes to legacy templates.

- [ ] **Step 4: Verify GREEN and commit**

Run: `python3 -m pytest examples/bevfusion/s1h-streaming/test_contract.py scripts/e2e_bevfusion_portal_submit_test.py -q && cd backend && go test ./spkrayjob ./api -run 'Test.*Streaming' -count=1`

Expected: PASS; existing `ray-ddp` tests remain green.

```bash
git add examples/bevfusion/s1h-streaming docs/S1H_STREAMING_ACCEPTANCE.md docs/BEVFUSION_END_TO_END_GUIDE.md && git commit -m "docs: add S1H streaming three-entry acceptance"
```

### Task 6: Safe production rollout and full-data validation

**Files:**
- Create: `deploy/overlays/distributed-dataset-publisher.yaml`
- Create: `ops/acceptance/s1h-streaming/run.sh`
- Create: `ops/acceptance/s1h-streaming/collect.sh`
- Create: `ops/acceptance/s1h-streaming/test_contract.sh`

- [ ] **Step 1: Write failing deployment contract**

```bash
assert_contains deploy/overlays/distributed-dataset-publisher.yaml 'distributed:'
assert_not_contains deploy/overlays/distributed-dataset-publisher.yaml 'rayClusterSpec'
assert_not_contains deploy/overlays/distributed-dataset-publisher.yaml 'rayjob'
```

- [ ] **Step 2: Verify RED**

Run: `bash ops/acceptance/s1h-streaming/test_contract.sh`

Expected: FAIL because rollout assets do not exist.

- [ ] **Step 3: Implement safe rollout scripts**

The overlay enables only versioning, streaming, and low-priority CPU-only distributed publishing. It never changes legacy image defaults, Kueue GPU quota, RayJob/RayCluster templates, PVCs, or namespaces. `run.sh` performs read-only health checks, creates one version, waits for `READY`, preflights it, then submits Portal, `spk-rayjob`, and native jobs serially. It stops before submission that conflicts with user workload capacity and never deletes resources.

- [ ] **Step 4: Verify local assets, then build/deploy**

Run: `bash ops/acceptance/s1h-streaming/test_contract.sh && helm template ray-platform helm/ray-train-platform -f deploy/overlays/distributed-dataset-publisher.yaml >/tmp/ray-platform-distributed.yaml && bash scripts/test-ray-runtime-images.sh`

Expected: all commands exit 0 and rendered resources are platform/publisher only.

Only after fresh `helm get values`, release status, and live Ray workload review, build and deploy with `--reuse-values` plus IDC/distributed overlays. Run three submissions serially and record task ID, code commit, image digest, dataset version, resource sizing, cache/spill metrics, outputs, and model exceptions separately.

## Plan self-review

- Tasks 1–3 cover resumed and incremental publishing; task 4 covers Ray Data, bounded NVMe, and separate Ray spilling; task 5 enforces three user submission paths; task 6 provides a no-delete, opt-in rollout.
- New behavior is feature-gated. Existing `ray-ddp`, mount, cache, `ray-data-stage`, RayJob/RayCluster, PVC, namespace, and user-task behavior stays unchanged.
- Worker input is a trusted control-plane plan. User-facing files omit object-store credentials and internal infrastructure identifiers.
