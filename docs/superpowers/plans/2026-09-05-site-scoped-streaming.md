# Site-scoped streaming development

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
