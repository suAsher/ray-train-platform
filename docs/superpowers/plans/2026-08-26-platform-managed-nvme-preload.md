# Platform-managed NVMe preload implementation plan

> **Goal:** Let a portal, `spk-rayjob`, or native Ray API user enable dual-NVMe dataset acceleration with submission parameters only. The platform stages the selected input on every worker before training and exposes the cached view through `PLATFORM_DATASET_PATH`; the user's model code does not copy files.

## Contract and safety boundary

- Keep `cache.mode=runtime` backward compatible: without `cache.preload`, only Ray temp/object spilling and explicit user cache writes use NVMe.
- Add `cache.preload=input`. It requires a governed input data space and a non-empty `input.path`; selecting an entire logical root is rejected before GPUs are allocated.
- Each worker receives two generic ephemeral PVCs, one on `/data1` and one on `/data2`, and a platform init container stages the selected read-only input across both disks.
- The training container sees `PLATFORM_DATASET_SOURCE_PATH` as the durable source and `PLATFORM_DATASET_PATH=/mnt/cache/dataset-view` as the active input. The original read-only mount remains present.
- The preloader fails closed on unsafe paths, symlinks, capacity exhaustion, timeout, or copy failure. Checkpoints and outputs never point at the cache.
- Head and submitter Pods do not preload datasets. Every worker preloads its own node-local copy before Ray starts training.

## Task 1: Domain, API, and native Ray metadata contract

**Files:**
- Modify: `backend/domain/training_job.go`
- Modify: `backend/domain/training_job_test.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/api/submission_service_test.go`
- Modify: `backend/rayapi/translator.go`
- Modify: `backend/rayapi/translator_test.go`

1. Add failing tests for `cache.preload=input`, invalid preload values, preload with cache off, and preload without a non-empty governed input path.
2. Add `CachePreloadMode` and normalize/validate it without changing existing runtime-cache submissions.
3. Add `platform.cache.preload` to native Ray metadata and reject unknown cache metadata.
4. Run focused Go tests and confirm green.

## Task 2: `spk-rayjob` parameter and project-file support

**Files:**
- Modify: `backend/spkrayjob/project.go`
- Modify: `backend/spkrayjob/cache.go`
- Modify: `backend/spkrayjob/command.go`
- Modify: `backend/spkrayjob/project_test.go`
- Modify: `backend/spkrayjob/command_workflow_test.go`

1. Add failing tests for `--cache-preload input`, `.spk-rayjob.yaml` `cache.preload`, override precedence, and invalid combinations.
2. Carry preload through merge, validation, defaults, and the final `JobSpec`.
3. Update CLI help with one parameter-only example.
4. Run focused CLI tests and confirm green.

## Task 3: Worker init-container staging and runtime path switch

**Files:**
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_cache_test.go`
- Modify: `images/source-materializer/Dockerfile`
- Add: `images/source-materializer/platform-stage-dataset.py`
- Modify: `examples/dataset-cache/stage_dataset.py`
- Modify: `examples/dataset-cache/stage_dataset_test.py`

1. Add failing renderer tests proving: worker-only init containers, both cache PVC mounts, read-only selected input mount, source/active path variables, no checkpoint/output cache rewrite, and unchanged legacy runtime mode.
2. Add failing Python tests for parallel copy, dual-disk distribution, atomic ready metadata, capacity guard, and repeat-run reuse.
3. Implement a non-root platform preloader in the existing pinned source-materializer image.
4. Render the preloader only for `preload=input`; give it bounded CPU/memory and a timeout, then switch the training container's dataset environment to the cached view.
5. Keep the example script compatible but mark it as a manual/legacy wrapper around the same staging contract.
6. Run focused Go and Python tests and confirm green.

## Task 4: Portal controls and copyable commands

**Files:**
- Modify: `frontend/src/composables/useJobForm.js`
- Modify: `frontend/src/components/job/StepRuntime.vue`
- Modify: `frontend/src/components/job/SubmitPreview.vue`
- Modify: `frontend/src/submission.js`
- Modify: `frontend/src/submission.test.js`
- Modify: `frontend/src/runtimeCacheUi.test.js`

1. Add failing tests for the automatic-preload switch, API payload, copied `spk-rayjob` command, copied/resubmitted jobs, and rejection when no exact input directory is selected.
2. Add a plain-language “自动预热所选输入到双 NVMe” control under runtime cache.
3. Explain cold-start cost, per-worker copy semantics, durable output, and that the selected input must be a dataset subdirectory rather than the whole public root.
4. Run all frontend tests and build.

## Task 5: Deployment contract and operations tests

**Files:**
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `ops/platform/test/ha-rollout-template-test.sh`
- Modify: `images/source-materializer/.dockerignore` if needed

1. Add failing image-content/build-contract tests proving the pinned source-materializer contains the preloader.
2. Keep one immutable image setting by extending the existing source-materializer image rather than introducing another mutable runtime dependency.
3. Verify Helm rendering and platform preflight tests.

## Task 6: Dedicated user guide and full verification

**Files:**
- Rewrite: `docs/NVME_CACHE_GUIDE.md`
- Modify: `examples/dataset-cache/README.md`

1. Document portal, `spk-rayjob`, `.spk-rayjob.yaml`, and native Ray API usage.
2. Document exact path semantics, cold/warm timing, capacity sizing, lifecycle/cleanup, limitations, troubleshooting, and how to compare direct TOS/FSX with NVMe.
3. Clearly distinguish automatic preload from legacy explicit staging and state that model code should read `PLATFORM_DATASET_PATH` (or receive it as a normal command parameter).
4. Run `go test ./...`, Python staging tests, all frontend tests, frontend build, Helm/template tests, shell contract tests, formatting, and secret scan.
5. Review the final diff for backward compatibility and production safety before any build/deploy operation.

