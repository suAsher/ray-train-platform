# Site-scoped streaming development

## Release status — 2026-09-05

The development-only scope below is historical. User subsequently approved
deployment while preserving running training, and explicitly approved pushing
main. Code commit `1d7c655` was released as `release-20260905-04`, Helm revision
**171**. Platform rollout and basic health checks passed; full-data/multi-GPU
acceptance is still pending.

| Component | Registry digest |
| --- | --- |
| backend | `sha256:0084784d54a06cd4d7dd47759a207a68ed3d6f022645a343c703bb8f55ba362f` |
| frontend | `sha256:f7279860690fb4a040788a79b256e4f2f009f3ab5e67b13922c9dc2f69b0a9d9` |
| spk-rayjob | `sha256:36d48679d3ed07a3d4ecb0fd5176045daf3ecc8f108a01add479120cd152f761` |
| dataset-publisher | `sha256:d4775f35c17d0f238ee97b386464f367ca58b75d19f49c1fc0dca1a217f54f15` |
| BEVFusion Ray 2.58 | `sha256:8defc2ea9982713b2e476ac40fb6fd33fd841979ea4de786f38555813fbde2a3` |

New shared, non-default catalogue entry:
`BEVFusion Ray Train 2.58 · 场地流式 release-20260905-04`,
ID `job-dddc152d4fe4413822a83f0e`. Old environments/defaults remain unchanged.
Helm runtimeCatalog values are release metadata, not the live image catalogue;
the new selectable environment was registered through the platform API.

Rollout evidence: migration 30 applied; backend/frontend/CLI pods Ready with
the expected image IDs; healthz HTTP 200; downloaded CLI checksum matched and
reported release-20260905-04 with `--dataset-sites` in help. The new training
image's isolated protocol import passed. Its 204 CPU tests passed after
isolating the generic test harness from the image's business prepare hook and
`/opt/bevfusion` example path; unisolated execution initially failed two tests.
No GPUs or production data were used for these tests.

`tenant-local/job-29dc380420222684984b87cf` remained RUNNING with unchanged
RayJob UID/spec and unchanged head/worker Pod UIDs/restart counts.
Build-host backups and override: `/root/release-20260905-04/`.

### Pending acceptance — manual follow-up, no hourly polling

- [ ] After the existing training finishes and resources are available, perform
  the limited fixture publication and multi-worker acceptance listed below.
- [ ] Verify site inventory and new manifest metadata before selecting sites;
  do not rewrite existing READY versions or start a full repack.
- [ ] Measure epoch coverage, recovery, NVMe pressure and actual throughput.

The scheduled checker is **PAUSED** at the user's request. Resume verification
when the user reports completion; this release does not claim training-path
acceptance.

## Approved scope

Develop locally before production verification. Do not publish datasets, change
running RayJobs, deploy images, or scan the public data tree recursively.

## Contract

- An optional canonical list `datasetRef.sites` selects sites within one pinned
  version. Empty means the existing full-version behavior.
- Persist the selection with job provenance and send it as
  `PLATFORM_DATASET_SITES_JSON` to the managed driver.
- Reference manifests expose `site_id` derived from trusted source metadata.
  Old manifests remain readable without a selection. A selected site that is
  unknown, unavailable, or lacks metadata fails closed.
- Filter reference rows before worker padding. Derive selected training counts
  from the manifest, never reuse the full-version count as the selected count.
- Preserve established splits and schema support for lidar and multimodal data.
- Bound row-group reuse and permit verified NVMe hits without a source lookup.

## Implementation and checks

1. Publisher: add site metadata to both manifest formats and test old input,
   malformed source paths, and distributed finalization.
2. Submission: validate/canonicalize site lists, persist immutable selection,
   pass runtime environment, expose UI selection, and test round trips.
3. Runtime: select sites before padding; test unknown sites, old manifests,
   multiple sites, selected counts, and no-selection compatibility on CPU.
4. I/O: test repeated row-group reads and cache hits with unavailable sources;
   maintain bounded memory, digest validation, and cache eviction safety.
5. Run affected suites, inspect the integrated diff, document remaining
   production/GPU verification requirements. Do not infer performance from
   mock-based tests.

## Deferred operational acceptance

Small CPU-only publication with explicit resource limits; verify site coverage
against a frozen inventory; then multi-GPU epoch coverage, restart, and I/O
measurements when GPUs are available. Existing versions and workloads remain
pinned. No full-data repack is part of this development turn.

## Development handoff

Branch: `codex/site-scoped-streaming`. This is local development, not a release.

- UI accepts explicit site codes; API `datasetRef.sites` and CLI
  `--dataset-sites cnfzhjyg,cnzshytg` carry the same canonical selection.
  Leaving sites empty retains full-version behavior.
- Migration `0030_training_dataset_sites.up.sql` persists selection separately
  from the pinned dataset/version/digest. Resume and rerun must retain scope.
- The generated launcher requires site-selection runtime protocol 1 for scoped
  jobs. Old runtime images must fail before training, never ignore the filter.
- New reference manifests contain conservative `site_id` metadata. Existing
  immutable READY versions are not rewritten: versions missing complete site
  metadata need a new publication before accepting selected-site jobs.
- Preflight reports full-version counts and pending manifest validation, not
  an estimated selected count. The driver verifies and counts selected train
  rows in bounded metadata batches before equal-worker padding.
- Epoch-local reference shuffle uses the restored epoch and a bounded buffer.
  This is not an unbounded global shuffle. Worker payload decoding remains in
  the resolver; this change does not move the complete decode pipeline into
  Ray Data operators.
- Parquet row-group reuse retains at most 128 MiB and 8 groups per resolver.
  Oversized groups bypass retention; transient decode/batch memory is additional.
  Verified disk cache hits work without a source-file stat. Parquet readers hold
  a shared cache lock against cooperative eviction.

### Remaining limits and acceptance order

1. Inspect a small trusted source index against `/mnt/storage/public` before
   publication: site inference recognizes `site/scene/...` and
   `labeled/site/scene/...` only. Unknown layouts deliberately remain unidentified.
   Do not recursively scan the shared tree or unpickle untrusted input.
2. Build new runtime and publisher images, validate isolated launcher imports,
   then publish an isolated CPU-only fixture with explicit CPU/memory/I/O limits.
   Do not modify existing jobs, versions, shared cache configuration, or mounts.
3. Check site coverage, train/val/test integrity and CBGS multiplicities. The UI
   currently takes manual codes; a catalogue-backed site picker and control-plane
   selected-count preflight are follow-up work.
4. When GPUs are available, test multiple workers, equal steps, resumed epochs,
   cache pressure/eviction and throughput. CPU tests do not establish distributed
   sample coverage, training performance or FUSE-outage tolerance.
5. Further throughput work may need bounded asynchronous payload prefetch and
   parallel download reservations: the existing global cache download lock still
   serializes misses. Legacy TAR readers do not use the new Parquet read lease;
   reused publisher receipts can retain mixed-site shards. Neither limitation
   is claimed fixed in this branch.

No full-data conversion, production migration, image rollout or GPU validation
has been performed as part of this development step.

### Local verification (2026-09-05)

- Backend `go test ./...`: passed (16 tested packages); local listener tests
  required execution outside the filesystem/network sandbox. `go build ./...`
  passed.
- Frontend `npm test`: 287 passed; `npm run build`: passed (bundle-size warnings).
- Runtime unittest suite: 204 passed.
- BEVFusion patch unittest suite: 69 passed.
- Publisher unittest suite: 119 passed, one optional TOS SDK test skipped.
- Python checks used Python 3.12 and real PyArrow 25 for Parquet fixtures;
  Ray orchestration tests use test doubles, not a live multi-worker cluster.
- Independent review's old-image fail-open finding was fixed and rechecked.
  `git diff --check` passed. No claim of 80% coverage or GPU acceptance is made.
