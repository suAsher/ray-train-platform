# Ray Train Managed Runtime Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade production to Ray 2.56.1 and KubeRay 1.6.2 while preserving running Ray 2.35 jobs, then add an opt-in Ray Train engine that owns distributed workers, metrics, checkpoints, and recovery.

**Architecture:** Keep one RayCluster per job and keep Kueue as the outer quota/gang scheduler. Add an immutable `trainingEngine` snapshot beside the existing execution topology so persisted `execution.mode=ray_train` records retain their current Actor + torchrun meaning. New managed jobs use a platform-owned `TorchTrainer` driver, shared TOS-backed checkpoints, job-scoped status callbacks, and the existing Loki/Prometheus/MLflow surfaces.

**Tech Stack:** Go 1.24, Gin, GORM/PostgreSQL, Kubernetes dynamic client, KubeRay RayJob v1, Kueue, Ray 2.56.1 Train V2, Python 3.10, PyTorch/DDP, Vue 3/Vite, Helm 3, Loki, Prometheus Operator, MLflow, VKE FSX/TOS, dual local NVMe.

---

## Work breakdown and file boundaries

The approved specification spans four sequential, independently testable releases:

1. **Foundation:** immutable engine/runtime contracts, image compatibility metadata, CLI/UI selection, and Ray 2.56.1-compatible DDP. No managed jobs are enabled yet.
2. **Managed execution:** `TorchTrainer`, framework adapters, checkpoint records, worker recovery, and outer RayCluster attempts behind a disabled feature flag.
3. **Data and observability:** preserve the existing `/mnt/data/*` contract, expose managed cache/Ray Data modes, and add worker/data/recovery views.
4. **Production rollout:** KubeRay 1.6.2 CRD/operator upgrade only during a zero-workload window, fault injection, BEVFusion acceptance, default switch, and rollback drill.

New files have one responsibility each:

- `backend/domain/training_engine.go`: public engine, Ray version, failure, checkpoint, and data-mode value objects.
- `backend/domain/training_checkpoint.go`: persisted checkpoint metadata and lifecycle rules.
- `backend/runtimecatalog/catalog.go`: resolve an engine-compatible image into an immutable runtime snapshot.
- `images/workspace/raytrain_runtime/managed_driver.py`: construct and run `TorchTrainer`.
- `images/workspace/raytrain_runtime/entrypoint.py`: execute supported Python entrypoints inside a Train worker process.
- `images/workspace/raytrain_runtime/reporting.py`: metrics/checkpoint reporting and job-scoped callbacks.
- `images/workspace/raytrain_runtime/mmcv_hook.py`: optional MMCV integration without coupling the generic driver to MMCV.
- `backend/api/job_train_events.go`: authenticated internal events from managed workers.
- `backend/api/job_checkpoints.go`: user-facing checkpoint list and resume contracts.
- `backend/observability/training_performance.go`: task-scoped CPU, memory, NCCL, data, and cache queries.
- `ops/kuberay/`: guarded CRD/operator upgrade and verification scripts.

## Release 1 — Dual-engine and version foundation

### Task 1: Add immutable training-engine and runtime value objects

**Files:**
- Create: `backend/domain/training_engine.go`
- Create: `backend/domain/training_engine_test.go`
- Modify: `backend/domain/training_job.go`
- Modify: `backend/domain/training_job_test.go`

- [ ] **Step 1: Write failing engine and policy validation tests**

```go
func TestTrainingEngineDefaultsOldJobsToRayDDP(t *testing.T) {
	if got := (TrainingEngine("")).Resolved(); got != TrainingEngineRayDDP {
		t.Fatalf("old job resolved to %q", got)
	}
}

func TestManagedTrainingPolicyIsBounded(t *testing.T) {
	policy := ManagedTrainingPolicy{
		MaxFailures: 2,
		Checkpoint: CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedEngineRejectsRay235(t *testing.T) {
	spec := validJobSpec()
	spec.TrainingEngine = TrainingEngineRayTrain
	spec.RayVersion = "2.35.0"
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "Ray 2.56.1") {
		t.Fatalf("expected managed-runtime version rejection, got %v", err)
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

Run: `cd backend && go test ./domain -run 'TrainingEngine|ManagedTraining|ManagedEngine' -count=1`

Expected: FAIL because `TrainingEngine`, `ManagedTrainingPolicy`, and the new `JobSpec` fields do not exist.

- [ ] **Step 3: Implement immutable value objects and validation**

```go
package domain

import (
	"fmt"
	"strings"
)

type TrainingEngine string

const (
	TrainingEngineRayDDP   TrainingEngine = "ray-ddp"
	TrainingEngineRayTrain TrainingEngine = "ray-train"
	RayVersionLegacy                      = "2.35.0"
	RayVersionProduction                  = "2.56.1"
	RayVersionCanary                      = "2.58.0"
)

func (engine TrainingEngine) Resolved() TrainingEngine {
	if strings.TrimSpace(string(engine)) == "" {
		return TrainingEngineRayDDP
	}
	return engine
}

type DataMode string

const (
	DataModeMount   DataMode = "mount"
	DataModeCache   DataMode = "cache"
	DataModeRayData DataMode = "ray-data"
)

type CheckpointPolicy struct {
	EveryEpochs int `json:"everyEpochs,omitempty"`
	KeepLatest  int `json:"keepLatest,omitempty"`
	KeepBest    int `json:"keepBest,omitempty"`
}

type ManagedTrainingPolicy struct {
	MaxFailures int              `json:"maxFailures,omitempty"`
	Checkpoint  CheckpointPolicy `json:"checkpoint,omitempty"`
}

func (policy ManagedTrainingPolicy) Validate() error {
	if policy.MaxFailures < 0 || policy.MaxFailures > 10 {
		return fmt.Errorf("maxFailures must be between 0 and 10")
	}
	if policy.Checkpoint.EveryEpochs < 0 || policy.Checkpoint.KeepLatest < 0 || policy.Checkpoint.KeepBest < 0 {
		return fmt.Errorf("checkpoint policy values must be non-negative")
	}
	return nil
}
```

Add these fields to `JobSpec`:

```go
TrainingEngine TrainingEngine        `json:"trainingEngine,omitempty"`
RayVersion     string                `json:"rayVersion,omitempty"`
Managed        ManagedTrainingPolicy `json:"managed,omitempty"`
DataMode       DataMode              `json:"dataMode,omitempty"`
ParentJobID    string                `json:"parentJobId,omitempty"`
```

Validation rules:

- missing `trainingEngine` means `ray-ddp` for persisted jobs;
- `ray-train` requires Ray 2.56.1 or 2.58.0 and `Managed.Validate()`;
- `ray-data` requires `ray-train`;
- `cache` requires the existing runtime cache with `preload=input`;
- `ParentJobID` must match the existing job ID format when present.

- [ ] **Step 4: Run domain tests and confirm GREEN**

Run: `cd backend && go test ./domain -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the domain contract**

```bash
git add backend/domain/training_engine.go backend/domain/training_engine_test.go backend/domain/training_job.go backend/domain/training_job_test.go
git commit -m "feat: add immutable training engine contract"
```

### Task 2: Persist runtime snapshots and recovery counters additively

**Files:**
- Create: `backend/db/migrations/0020_training_runtime_metadata.up.sql`
- Modify: `backend/db/migration_targets_test.go`
- Modify: `backend/db/postgres_integration_test.go`
- Modify: `backend/repositories/jobs.go`
- Modify: `backend/repositories/jobs_record_test.go`
- Modify: `backend/repositories/jobs_test.go`
- Modify: `backend/domain/training_job.go`

- [ ] **Step 1: Write failing migration and repository round-trip tests**

```go
func TestJobRecordRoundTripsManagedRuntimeMetadata(t *testing.T) {
	job := fixtureJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.ClusterAttempt = 2
	job.WorkerRestartCount = 3
	record, err := newJobRecord(job)
	if err != nil {
		t.Fatal(err)
	}
	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayTrain || got.ClusterAttempt != 2 || got.WorkerRestartCount != 3 {
		t.Fatalf("runtime metadata lost: %+v", got)
	}
}
```

Extend the integration test’s required column list with `training_engine`, `ray_version`,
`cluster_attempt`, `worker_restart_count`, `resume_checkpoint_id`, and `parent_job_id`.

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd backend && go test ./db ./repositories -run 'Migration|RuntimeMetadata|RoundTripsManaged' -count=1`

Expected: FAIL because migration 0020 and record fields do not exist.

- [ ] **Step 3: Add an additive SQL migration**

```sql
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS training_engine TEXT NOT NULL DEFAULT 'ray-ddp';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS ray_version TEXT NOT NULL DEFAULT '2.35.0';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS cluster_attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS worker_restart_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS resume_checkpoint_id TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS parent_job_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS training_jobs_engine_state_idx
  ON training_jobs(training_engine, observed_state);
CREATE INDEX IF NOT EXISTS training_jobs_parent_idx
  ON training_jobs(parent_job_id) WHERE parent_job_id <> '';
```

- [ ] **Step 4: Map the new columns without changing `spec_json` compatibility**

Add fields to `JobRecord` and `TrainingJob`, then map them in `newJobRecord()` and
`toDomain()`. For old SQLite fixtures whose explicit columns are empty, preserve the domain
fallback:

```go
engine := domain.TrainingEngine(strings.TrimSpace(r.TrainingEngine)).Resolved()
rayVersion := strings.TrimSpace(r.RayVersion)
if rayVersion == "" {
	rayVersion = domain.RayVersionLegacy
}
spec.TrainingEngine = engine
spec.RayVersion = rayVersion
```

- [ ] **Step 5: Run migration and repository tests**

Run: `cd backend && go test ./db ./repositories -count=1`

Expected: PASS.

- [ ] **Step 6: Commit additive persistence**

```bash
git add backend/db/migrations/0020_training_runtime_metadata.up.sql backend/db backend/repositories/jobs.go backend/repositories/jobs_record_test.go backend/repositories/jobs_test.go backend/domain/training_job.go
git commit -m "feat: persist training runtime metadata"
```

### Task 3: Add Ray compatibility metadata to the image catalog

**Files:**
- Create: `backend/db/migrations/0021_image_runtime_compatibility.up.sql`
- Modify: `backend/domain/image.go`
- Modify: `backend/domain/image_normalize_test.go`
- Modify: `backend/repositories/images.go`
- Modify: `backend/repositories/images_test.go`
- Modify: `backend/api/images.go`
- Modify: `backend/api/images_test.go`
- Modify: `frontend/src/components/admin/CatalogPanel.vue`
- Modify: `frontend/src/imageCatalogAdmin.test.js`

- [ ] **Step 1: Write failing catalog compatibility tests**

```go
func TestPlatformImageValidatesEngineCompatibility(t *testing.T) {
	image := PlatformImage{
		Name: "Ray Train 2.56", Reference: "registry.example/train@sha256:" + strings.Repeat("a", 64),
		Kind: ImageKindTraining, RayVersion: RayVersionProduction,
		SupportedEngines: []TrainingEngine{TrainingEngineRayTrain},
	}
	if err := image.Validate(); err != nil {
		t.Fatal(err)
	}
}
```

API tests must prove that only a super administrator can publish a shared runtime and that
unknown Ray versions or duplicate engine names return `INVALID_IMAGE`.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `cd backend && go test ./domain ./repositories ./api -run 'Image.*Compatibility|PlatformImageValidates' -count=1`

Expected: FAIL because image runtime metadata is absent.

- [ ] **Step 3: Add the image migration and immutable fields**

```sql
ALTER TABLE platform_images ADD COLUMN IF NOT EXISTS ray_version TEXT NOT NULL DEFAULT '2.35.0';
ALTER TABLE platform_images ADD COLUMN IF NOT EXISTS supported_engines JSONB NOT NULL DEFAULT '["ray-ddp"]'::jsonb;
```

```go
type PlatformImage struct {
	// existing fields remain unchanged
	RayVersion       string           `json:"rayVersion"`
	SupportedEngines []TrainingEngine `json:"supportedEngines"`
}

func (image PlatformImage) Supports(engine TrainingEngine) bool {
	for _, candidate := range image.SupportedEngines {
		if candidate == engine.Resolved() {
			return true
		}
	}
	return false
}
```

Clone slices when mapping domain and repository values. Serialize `supported_engines` with
`encoding/json`; never retain a caller-owned slice.

- [ ] **Step 4: Expose administrator inputs in the catalog UI**

Add a Ray-version select with `2.35.0`, `2.56.1`, and `2.58.0`, plus engine checkboxes.
Disable `ray-train` when version `2.35.0` is selected. Submit this shape:

```js
{
  name: form.name,
  reference: form.reference,
  kind: form.kind,
  rayVersion: form.rayVersion,
  supportedEngines: [...form.supportedEngines],
  shared: Boolean(form.shared),
}
```

- [ ] **Step 5: Run backend and frontend tests**

Run: `cd backend && go test ./domain ./repositories ./api -count=1`

Run: `cd frontend && npm test -- src/imageCatalogAdmin.test.js`

Expected: PASS.

- [ ] **Step 6: Commit image compatibility metadata**

```bash
git add backend/db/migrations/0021_image_runtime_compatibility.up.sql backend/domain/image.go backend/domain/image_normalize_test.go backend/repositories/images.go backend/repositories/images_test.go backend/api/images.go backend/api/images_test.go frontend/src/components/admin/CatalogPanel.vue frontend/src/imageCatalogAdmin.test.js
git commit -m "feat: catalog ray runtime compatibility"
```

### Task 4: Resolve the engine and Ray version at submission time

**Files:**
- Create: `backend/runtimecatalog/catalog.go`
- Create: `backend/runtimecatalog/catalog_test.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/api/submission_service_test.go`
- Modify: `backend/api/platform_limits.go`
- Modify: `backend/api/platform_limits_test.go`
- Modify: `backend/config/config.go`
- Modify: `backend/config/config_test.go`

- [ ] **Step 1: Write failing resolver and submission tests**

```go
func TestResolveRejectsManagedEngineOnLegacyImage(t *testing.T) {
	image := domain.PlatformImage{Reference: pinnedImage(), RayVersion: domain.RayVersionLegacy,
		SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP}}
	_, err := Resolve(image, domain.TrainingEngineRayTrain, Policy{ManagedEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "does not support ray-train") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSubmitSnapshotsCatalogRuntime(t *testing.T) {
	// Arrange the image store to return a Ray 2.56.1 managed image.
	// Submit with trainingEngine=ray-train.
	// Assert persisted Spec.RayVersion is 2.56.1 and cannot come from user input.
}
```

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `cd backend && go test ./runtimecatalog ./api -run 'Resolve|SnapshotsCatalogRuntime' -count=1`

Expected: FAIL because the package and runtime policy are absent.

- [ ] **Step 3: Implement a pure runtime resolver**

```go
type Policy struct {
	ManagedEnabled bool
	CanaryEnabled  bool
	// Canary tenant IDs are held privately and copied by NewPolicy/Clone.
}

type Snapshot struct {
	Engine       domain.TrainingEngine
	RayVersion   string
	ImageDigest  string
}

func Resolve(image domain.PlatformImage, requested domain.TrainingEngine, policy Policy) (Snapshot, error) {
	engine := requested.Resolved()
	if engine == domain.TrainingEngineRayTrain && !policy.ManagedEnabled {
		return Snapshot{}, fmt.Errorf("Ray Train managed engine is not enabled")
	}
	if !image.Supports(engine) {
		return Snapshot{}, fmt.Errorf("image %q does not support %s", image.Name, engine)
	}
	if image.RayVersion == domain.RayVersionCanary && !policy.CanaryEnabled {
		return Snapshot{}, fmt.Errorf("Ray %s is restricted to canary tenants", image.RayVersion)
	}
	return Snapshot{Engine: engine, RayVersion: image.RayVersion, ImageDigest: image.Reference}, nil
}
```

`SubmissionService.Submit()` must compute `policy.EffectiveForTenant(principal.TenantID)`, look up
the selected image, call this resolver, and overwrite
`Spec.TrainingEngine`, `Spec.RayVersion`, and normalized `Spec.Image` before validation. A request
cannot claim a different Ray version than the catalog.

- [ ] **Step 4: Add disabled-by-default feature flags and platform capabilities**

Parse:

```go
RayTrainManagedEnabled bool
RayTrainCanaryEnabled  bool
RayTrainCanaryTenants  []string
```

from `RAY_TRAIN_MANAGED_ENABLED=false`, `RAY_TRAIN_CANARY_ENABLED=false`, and the comma-separated
`RAY_TRAIN_CANARY_TENANTS`. Empty canary tenants deny canary to every tenant. Add the engine list
and production/canary versions to `/api/v1/limits` so CLI and Portal do not hard-code availability.

- [ ] **Step 5: Run API and configuration tests**

Run: `cd backend && go test ./runtimecatalog ./api ./config -count=1`

Expected: PASS.

- [ ] **Step 6: Commit runtime resolution**

```bash
git add backend/runtimecatalog backend/api/submission_service.go backend/api/submission_service_test.go backend/api/platform_limits.go backend/api/platform_limits_test.go backend/config/config.go backend/config/config_test.go
git commit -m "feat: resolve immutable ray runtimes"
```

### Task 5: Render per-job Ray versions and preserve the old launcher contract

**Files:**
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_test.go`
- Create: `backend/k8s/rayjob_managed_test.go`
- Modify: `backend/k8s/rayjob_entrypoint_test.go`
- Modify: `backend/k8s/rayjob_lifecycle_test.go`

- [ ] **Step 1: Write renderer regression tests before implementation**

```go
func TestLegacyRayTrainExecutionModeStillUsesActorLauncher(t *testing.T) {
	job := validJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayDDP
	job.Spec.Execution.Mode = domain.ExecutionModeRayTrain
	resource := mustRender(t, job)
	if got := nestedString(t, resource.Object, "spec", "entrypoint"); !strings.HasPrefix(got, "raytrain-launch --mode ray_train") {
		t.Fatalf("legacy serialized mode changed meaning: %s", got)
	}
}

func TestManagedJobUsesPerJobVersionAndManagedDriver(t *testing.T) {
	job := validJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	resource := mustRender(t, job)
	if got := nestedString(t, resource.Object, "spec", "rayClusterSpec", "rayVersion"); got != "2.56.1" {
		t.Fatalf("wrong Ray version: %s", got)
	}
	entrypoint := nestedString(t, resource.Object, "spec", "entrypoint")
	if !strings.HasPrefix(entrypoint, "raytrain-managed") {
		t.Fatalf("wrong managed entrypoint: %s", entrypoint)
	}
}
```

- [ ] **Step 2: Run renderer tests and confirm RED**

Run: `cd backend && go test ./k8s -run 'LegacyRayTrainExecution|ManagedJobUsesPerJob' -count=1`

Expected: managed test FAIL; legacy regression test PASS.

- [ ] **Step 3: Route by `TrainingEngine` before the existing execution mode**

```go
func trainingEntrypoint(spec domain.JobSpec) []string {
	command := append(append([]string{}, spec.Entrypoint.Command...), spec.Entrypoint.Args...)
	if spec.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain {
		return append([]string{
			"raytrain-managed",
			"--nodes", strconv.Itoa(spec.Resources.WorkerReplicas),
			"--gpus-per-node", strconv.Itoa(spec.Resources.GPUsPerWorker),
			"--cpus-per-node", strconv.FormatInt(spec.Resources.CPUPerWorker, 10),
			"--max-failures", strconv.Itoa(spec.Managed.MaxFailures),
			"--",
		}, command...)
	}
	return executionProfileEntrypoint(spec)
}
```

Set the RayCluster `rayVersion` from the immutable job snapshot. Keep `RenderOptions.RayVersion`
only as a fallback for persisted rows created before migration 0020. Add
`RAY_TRAIN_V2_ENABLED=1`, `PLATFORM_TRAINING_ENGINE=ray-train`, and the job ID to the managed
runtime environment.

- [ ] **Step 4: Verify old RayJob objects remain byte-for-byte stable in relevant fields**

Extend lifecycle tests to render a pre-upgrade `JobSpec` with empty engine/version and assert:

- legacy `rayVersion` remains 2.35.0;
- existing `raytrain-launch` command is unchanged;
- existing Actor + torchrun placement remains `STRICT_SPREAD` across the requested worker nodes;
- no managed environment or callback Secret is added;
- Kueue suspend and cleanup TTL behavior is unchanged.

- [ ] **Step 5: Run all Kubernetes renderer tests**

Run: `cd backend && go test ./k8s -count=1`

Expected: PASS.

- [ ] **Step 6: Commit dual rendering**

```bash
git add backend/k8s/rayjob.go backend/k8s/rayjob_test.go backend/k8s/rayjob_managed_test.go backend/k8s/rayjob_entrypoint_test.go backend/k8s/rayjob_lifecycle_test.go
git commit -m "feat: render dual ray training engines"
```

### Task 6: Add engine selection to `spk-rayjob` and native Ray metadata

**Files:**
- Modify: `backend/spkrayjob/command.go`
- Modify: `backend/spkrayjob/project.go`
- Modify: `backend/spkrayjob/render.go`
- Modify: `backend/spkrayjob/command_workflow_test.go`
- Modify: `backend/spkrayjob/project_test.go`
- Modify: `backend/rayapi/translator.go`
- Modify: `backend/rayapi/translator_test.go`
- Modify: `backend/rayapi/contract_server_test.go`

- [ ] **Step 1: Write failing CLI/YAML/metadata tests**

```go
func TestSubmitCarriesManagedEngineWithoutRayVersionOverride(t *testing.T) {
	command := newSubmitCommand(fakeClient)
	command.SetArgs([]string{"--engine", "ray-train", "--entrypoint", "python train.py"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if submitted.TrainingEngine != domain.TrainingEngineRayTrain || submitted.RayVersion != "" {
		t.Fatalf("CLI must select an engine but not forge a Ray version: %+v", submitted)
	}
}
```

Add a `.spk-rayjob.yaml` fixture with `engine: ray-train`, and a native metadata test for
`platform.training.engine=ray-train`.

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd backend && go test ./spkrayjob ./rayapi -run 'ManagedEngine|TrainingEngine|CarriesManaged' -count=1`

Expected: FAIL because the parameter and metadata key do not exist.

- [ ] **Step 3: Implement one engine parameter across all submission paths**

Add `--engine ray-ddp|ray-train`, project YAML `engine`, and metadata key
`platform.training.engine`. Do not expose a public `--ray-version`; the backend resolves it from
the selected image. Reject unknown `platform.training.*` metadata keys.

```go
const metadataTrainingEngine = "platform.training.engine"

engine := domain.TrainingEngine(strings.TrimSpace(metadata[metadataTrainingEngine])).Resolved()
if engine != domain.TrainingEngineRayDDP && engine != domain.TrainingEngineRayTrain {
	return domain.JobSpec{}, fmt.Errorf("unsupported training engine %q", engine)
}
spec.TrainingEngine = engine
```

- [ ] **Step 4: Update help and command rendering**

The help text must describe `ray-ddp` as compatible Actor/torchrun orchestration and
`ray-train` as managed workers/checkpoints. Render `--engine` in copyable commands only when the
server advertises both engines.

- [ ] **Step 5: Run CLI and native API suites**

Run: `cd backend && go test ./spkrayjob ./rayapi -count=1`

Expected: PASS.

- [ ] **Step 6: Commit submission clients**

```bash
git add backend/spkrayjob backend/rayapi
git commit -m "feat: submit managed ray train jobs"
```

### Task 7: Add an engine selector to the Portal without changing the default

**Files:**
- Modify: `frontend/src/composables/useJobForm.js`
- Modify: `frontend/src/composables/useJobForm.test.js`
- Modify: `frontend/src/submission.js`
- Modify: `frontend/src/submission.test.js`
- Modify: `frontend/src/components/job/StepRuntime.vue`
- Modify: `frontend/src/components/job/SubmitPreview.vue`
- Modify: `frontend/src/views/Job/CreateJob.vue`
- Modify: `frontend/src/views/Job/JobDetail.vue`
- Create: `frontend/src/trainingEngine.js`
- Create: `frontend/src/trainingEngine.test.js`

- [ ] **Step 1: Write failing form, payload, and resubmission tests**

```js
test('managed engine is opt-in and carries managed policy', () => {
  const payload = buildJobSpec({
    ...validForm,
    trainingEngine: 'ray-train',
    maxFailures: 2,
    checkpointEveryEpochs: 1,
    checkpointKeepLatest: 3,
    checkpointKeepBest: 1,
  }, limits)
  assert.equal(payload.trainingEngine, 'ray-train')
  assert.deepEqual(payload.managed, {
    maxFailures: 2,
    checkpoint: { everyEpochs: 1, keepLatest: 3, keepBest: 1 },
  })
})
```

Test that missing engine stays `ray-ddp`, unavailable managed mode is disabled, and “再来一次”
preserves the previous immutable engine while allowing “从 Checkpoint 续训” to create a child job.

- [ ] **Step 2: Run frontend tests and confirm RED**

Run: `cd frontend && npm test -- src/trainingEngine.test.js src/submission.test.js src/composables/useJobForm.test.js`

Expected: FAIL because the new form fields and helpers are absent.

- [ ] **Step 3: Add plain-language engine controls**

Use an immutable helper:

```js
export const normalizeTrainingEngine = (value, capabilities = {}) => {
  const requested = value === 'ray-train' ? 'ray-train' : 'ray-ddp'
  if (requested === 'ray-train' && !capabilities.managedEnabled) return 'ray-ddp'
  return requested
}
```

Render two cards:

- “Ray 编排 DDP”：default, existing code, platform executes torchrun where needed;
- “Ray Train 托管”：worker recovery, native metrics, checkpoints; disabled with the backend’s
  reason when the feature flag or selected image is incompatible.

Only managed mode reveals max failures and checkpoint retention controls.

- [ ] **Step 4: Show immutable runtime details on job detail**

Display engine, Ray version, image digest, worker count, World Size, cluster attempt, restart
count, and resume source. Do not relabel old `execution.mode=ray_train` jobs as managed unless
their new `trainingEngine` is explicitly `ray-train`.

- [ ] **Step 5: Run all frontend tests and production build**

Run: `cd frontend && npm test`

Run: `cd frontend && npm run build`

Expected: tests PASS and Vite exits 0.

- [ ] **Step 6: Commit the Portal engine flow**

```bash
git add frontend/src
git commit -m "feat: expose ray train engine selection"
```

### Task 8: Build Ray 2.56.1 runtime images beside Ray 2.35

**Files:**
- Modify: `images/train-pytorch/Dockerfile`
- Modify: `images/workspace/Dockerfile`
- Modify: `images/workspace/Dockerfile.test.sh`
- Modify: `build-image.sh`
- Modify: `scripts/test-go-builder-path-contract.sh`
- Create: `scripts/test-ray-runtime-images.sh`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/values-prod.yaml.example`

- [ ] **Step 1: Write failing Dockerfile and build-matrix contract tests**

```bash
grep -F 'ARG RAY_VERSION=2.56.1' images/train-pytorch/Dockerfile
grep -F 'ray[train,tune]==${RAY_VERSION}' images/train-pytorch/Dockerfile
grep -F 'python3 -c' images/train-pytorch/Dockerfile
grep -F 'RAY_RUNTIME_VARIANTS' build-image.sh
```

The test must also prove that no command rewrites or deletes the Ray 2.35 image tags.

- [ ] **Step 2: Run contract tests and confirm RED**

Run: `bash images/workspace/Dockerfile.test.sh && bash scripts/test-ray-runtime-images.sh`

Expected: FAIL because Ray 2.56.1 matrix support is absent.

- [ ] **Step 3: Parameterize Ray installation and verify the image at build time**

```dockerfile
ARG RAY_VERSION=2.56.1
RUN python3 -m pip install --no-cache-dir "ray[train,tune]==${RAY_VERSION}" \
 && python3 -c "import ray; assert ray.__version__ == '${RAY_VERSION}', ray.__version__" \
 && python3 -m pip check
```

Build distinct immutable targets for `pytorch-ray-ddp`, `pytorch-ray-train`, and workspace.
The managed image sets `RAY_TRAIN_V2_ENABLED=1`; the DDP image contains the same Ray runtime but
keeps the existing launcher as its default platform path.

- [ ] **Step 4: Add a runtime smoke script**

`scripts/test-ray-runtime-images.sh` must run each newly built image and assert:

```bash
python3 -c 'import ray, ray.train, torch; print(ray.__version__, torch.__version__)'
ray --version
test -x /usr/local/bin/raytrain-launch
test -x /usr/local/bin/raytrain-managed
```

It accepts exact image references through environment variables and exits before running if any
reference is empty.

- [ ] **Step 5: Run local build contracts**

Run: `bash scripts/test-ray-runtime-images.sh --contract-only`

Run: `bash images/workspace/Dockerfile.test.sh`

Expected: PASS.

- [ ] **Step 6: Commit the side-by-side image definitions**

```bash
git add images/train-pytorch/Dockerfile images/workspace/Dockerfile images/workspace/Dockerfile.test.sh build-image.sh scripts/test-go-builder-path-contract.sh scripts/test-ray-runtime-images.sh helm/ray-train-platform/values.yaml helm/ray-train-platform/values-prod.yaml.example
git commit -m "build: add ray 2.56 runtime variants"
```

## Release 2 — Ray Train managed execution

### Task 9: Implement the platform-owned `TorchTrainer` driver

**Task 6 compatibility gate:** both `spk-rayjob` and the native Ray Jobs API preserve the
existing working-directory contract by serializing the user's entrypoint as `/bin/sh -lc <text>`.
Before enabling `ray-train`, Task 9 must convert that exact legacy representation into a tested,
safe Python argv form supporting only `python file.py ...` and `python -m module ...`. It must
reject shell operators, arbitrary executables, and nested `torchrun` before allocating GPUs. Do
not split the shell string with an ad-hoc parser or pass `/bin/sh -lc` into Train workers.

The `raytrain-managed` parser must consume `--checkpoint-every-epochs`,
`--checkpoint-keep-latest`, and `--checkpoint-keep-best` before the `--` argument separator and
store all three values in the immutable `DriverConfig`. The factory must use both retention
values in `CheckpointConfig`; `every_epochs` is the explicit handoff contract for Task 10's
framework-neutral reporting and MMCV adapter, which must use it to decide checkpoint epochs.
Tests must prove non-default and boundary values survive parsing without being replaced by
driver defaults.

**Files:**
- Create: `images/workspace/raytrain_runtime/__init__.py`
- Create: `images/workspace/raytrain_runtime/entrypoint.py`
- Create: `images/workspace/raytrain_runtime/managed_driver.py`
- Create: `images/workspace/raytrain_runtime/test_entrypoint.py`
- Create: `images/workspace/raytrain_runtime/test_managed_driver.py`
- Create: `images/workspace/raytrain-managed`
- Modify: `images/workspace/Dockerfile`
- Modify: `images/train-pytorch/Dockerfile`

- [ ] **Step 1: Write failing pure-Python entrypoint tests**

```python
class EntrypointTest(unittest.TestCase):
    def test_parse_python_script_entrypoint(self):
        parsed = parse_python_entrypoint(["python", "tools/train.py", "--epochs", "2"])
        self.assertEqual(parsed.kind, "path")
        self.assertEqual(parsed.target, "tools/train.py")
        self.assertEqual(parsed.argv, ["tools/train.py", "--epochs", "2"])

    def test_rejects_nested_torchrun(self):
        with self.assertRaisesRegex(ValueError, "must not contain torchrun"):
            parse_python_entrypoint(["torchrun", "train.py"])
```

Support `python file.py ...` and `python -m module ...`; reject shell operators, nested torchrun,
and arbitrary executables before GPUs are allocated.

- [ ] **Step 2: Run tests and confirm RED**

Run: `python3 -m unittest discover -s images/workspace/raytrain_runtime -p 'test_*.py' -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement in-process Python execution**

```python
@dataclasses.dataclass(frozen=True)
class PythonEntrypoint:
    kind: str
    target: str
    argv: tuple[str, ...]

def execute(entrypoint: PythonEntrypoint) -> None:
    previous = tuple(sys.argv)
    try:
        sys.argv = list(entrypoint.argv)
        if entrypoint.kind == "path":
            runpy.run_path(entrypoint.target, run_name="__main__")
        else:
            runpy.run_module(entrypoint.target, run_name="__main__", alter_sys=True)
    finally:
        sys.argv = list(previous)
```

Execution stays inside the Ray Train worker process. Do not start a child `torchrun`, because
that would create a second process group outside Ray Train’s ownership.

- [ ] **Step 4: Implement a deterministic Trainer factory**

```python
def build_trainer(config: DriverConfig):
    workers = config.nodes * config.gpus_per_node
    cpus_per_worker = max(1, config.cpus_per_node // config.gpus_per_node)
    return TorchTrainer(
        train_loop_per_worker=train_loop,
        train_loop_config={"entrypoint": dataclasses.asdict(config.entrypoint)},
        scaling_config=ScalingConfig(
            num_workers=workers,
            use_gpu=True,
            resources_per_worker={"CPU": cpus_per_worker},
            placement_strategy="PACK",
        ),
        run_config=RunConfig(
            name=config.job_id,
            storage_path=config.storage_path,
            failure_config=FailureConfig(max_failures=config.max_failures),
            checkpoint_config=CheckpointConfig(
                num_to_keep=config.keep_latest,
                checkpoint_score_attribute=config.best_metric or None,
                checkpoint_score_order=config.best_mode,
            ),
        ),
    )
```

`storage_path` must resolve below `/mnt/data/output/.platform/ray-train`; reject any path outside
`PLATFORM_OUTPUT_PATH`.

- [ ] **Step 5: Test the factory without creating a cluster**

Patch `TorchTrainer`, `ScalingConfig`, and `RunConfig` with fakes. Assert 2 nodes × 8 GPUs creates
16 workers, 64 node CPUs yields 8 CPUs per Train worker, `max_failures=2` reaches
`FailureConfig`, both retention values reach `CheckpointConfig`, and `every_epochs` remains
available in `DriverConfig` for the Task 10 reporting/MMCV checkpoint schedule.

- [ ] **Step 6: Run Python tests and image contract tests**

Run: `python3 -m unittest discover -s images/workspace/raytrain_runtime -p 'test_*.py' -v`

Run: `bash images/workspace/Dockerfile.test.sh`

Expected: PASS.

- [ ] **Step 7: Commit the managed driver**

```bash
git add images/workspace/raytrain_runtime images/workspace/raytrain-managed images/workspace/Dockerfile images/train-pytorch/Dockerfile
git commit -m "feat: add ray train managed driver"
```

### Task 10: Add framework-neutral reporting and the MMCV adapter

**Files:**
- Create: `images/workspace/raytrain_runtime/reporting.py`
- Create: `images/workspace/raytrain_runtime/test_reporting.py`
- Create: `images/workspace/raytrain_runtime/mmcv_hook.py`
- Create: `images/workspace/raytrain_runtime/test_mmcv_hook.py`
- Create: `examples/bevfusion/patches/ray_train_managed.py`
- Create: `examples/bevfusion/patches/ray_train_managed_test.py`
- Modify: `examples/bevfusion/patches/apply-platform-runtime.py`

- [ ] **Step 1: Write failing checkpoint integrity and rank tests**

```python
def test_only_rank_zero_attaches_checkpoint(fake_train):
    report_metrics({"loss": 1.0}, checkpoint_dir="/tmp/checkpoint", world_rank=1)
    assert fake_train.report.call_args.kwargs["checkpoint"] is None

def test_checkpoint_manifest_is_complete_before_report(tmp_path):
    checkpoint = tmp_path / "epoch-1"
    checkpoint.mkdir()
    (checkpoint / "model.pth").write_bytes(b"model")
    finalize_checkpoint(checkpoint, {"epoch": 1, "step": 100})
    manifest = json.loads((checkpoint / "manifest.json").read_text())
    assert manifest["complete"] is True
    assert manifest["files"][0]["sha256"]
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `python3 -m unittest images.workspace.raytrain_runtime.test_reporting images.workspace.raytrain_runtime.test_mmcv_hook -v`

Expected: FAIL because reporting and hook modules do not exist.

- [ ] **Step 3: Implement reporting with immutable metric copies**

```python
def report_metrics(metrics, checkpoint_dir=None, world_rank=None):
    clean = {str(key): float(value) for key, value in dict(metrics).items() if _finite(value)}
    checkpoint = None
    if checkpoint_dir and (world_rank if world_rank is not None else train.get_context().get_world_rank()) == 0:
        checkpoint = train.Checkpoint.from_directory(str(checkpoint_dir))
    train.report(clean, checkpoint=checkpoint)
```

Every worker calls `train.report()` the same number of times. Non-zero ranks pass no checkpoint.
Reject incomplete manifests and non-finite metrics instead of sending NaN to Ray or MLflow.

- [ ] **Step 4: Implement an optional MMCV Hook**

The hook must import without MMCV installed and register only when MMCV is present. At configured
intervals it copies scalar values from `runner.log_buffer.output`; at checkpoint epochs Rank 0
writes model, optimizer, scheduler, AMP, epoch, step, data version, code SHA, image digest, and
World Size before reporting.

```python
def after_train_iter(self, runner):
    if not self.every_n_iters(runner, self.interval):
        return
    metrics = extract_scalar_metrics(dict(runner.log_buffer.output))
    report_metrics(metrics, world_rank=get_world_rank())
```

- [ ] **Step 5: Add a BEVFusion patch that does not rewrite the training loop**

The patch adds the custom hook and changes distributed initialization to:

```python
if distributed and not torch.distributed.is_initialized():
    init_dist(args.launcher, **cfg.dist_params)
```

This prevents MMCV from initializing a second process group after Ray Train has initialized one.

- [ ] **Step 6: Run adapter and existing BEVFusion patch tests**

Run: `python3 -m unittest discover -s images/workspace/raytrain_runtime -p 'test_*.py' -v`

Run: `python3 -m unittest discover -s examples/bevfusion/patches -p '*_test.py' -v`

Expected: PASS.

- [ ] **Step 7: Commit reporting adapters**

```bash
git add images/workspace/raytrain_runtime examples/bevfusion/patches
git commit -m "feat: report managed training checkpoints"
```

### Task 11: Add authenticated managed-run events and checkpoint records

**Files:**
- Create: `backend/db/migrations/0022_training_checkpoints.up.sql`
- Create: `backend/domain/training_checkpoint.go`
- Create: `backend/domain/training_checkpoint_test.go`
- Create: `backend/repositories/training_checkpoints.go`
- Create: `backend/repositories/training_checkpoints_test.go`
- Create: `backend/api/job_train_events.go`
- Create: `backend/api/job_train_events_test.go`
- Create: `backend/api/job_checkpoints.go`
- Create: `backend/api/job_checkpoints_test.go`
- Modify: `backend/main.go`
- Modify: `backend/k8s/secrets.go`
- Modify: `backend/k8s/secrets_test.go`
- Modify: `backend/k8s/rayjob.go`

- [ ] **Step 1: Write failing ownership, replay, and completeness tests**

```go
func TestTrainEventRejectsAnotherJobsToken(t *testing.T) {
	response := postTrainEvent(t, "job-a", tokenFor("job-b"), `{"type":"WORKER_GROUP_STARTED","generation":2}`)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestCheckpointListExcludesIncompleteRows(t *testing.T) {
	items, err := repository.ListUsableCheckpoints(ctx, "tenant-a", "user-a", "job-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if !item.Complete {
			t.Fatalf("incomplete checkpoint leaked: %+v", item)
		}
	}
}
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd backend && go test ./domain ./repositories ./api ./k8s -run 'TrainEvent|Checkpoint' -count=1`

Expected: FAIL because the event and checkpoint contracts are absent.

- [ ] **Step 3: Add checkpoint and event-token tables**

```sql
CREATE TABLE IF NOT EXISTS training_checkpoints (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  epoch BIGINT NOT NULL DEFAULT 0,
  step BIGINT NOT NULL DEFAULT 0,
  object_path TEXT NOT NULL,
  metric_name TEXT NOT NULL DEFAULT '',
  metric_value DOUBLE PRECISION,
  complete BOOLEAN NOT NULL DEFAULT FALSE,
  is_best BOOLEAN NOT NULL DEFAULT FALSE,
  manifest_sha256 TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS training_job_event_tokens (
  job_id TEXT PRIMARY KEY REFERENCES training_jobs(id) ON DELETE CASCADE,
  token_sha256 TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL
);
```

- [ ] **Step 4: Generate and mount a job-scoped token**

Use `crypto/rand` for 32 bytes, store only SHA-256 in PostgreSQL, and create a Secret named from
the job ID. Mount the token only in managed driver/worker Pods. The endpoint accepts event types
`WORKER_GROUP_STARTED`, `CHECKPOINT_COMPLETE`, and `TRAINING_PROGRESS`, validates monotonic
generation/epoch/step, uses constant-time comparison, and rate-limits each job.

```go
func tokenDigest(token []byte) string {
	digest := sha256.Sum256(token)
	return hex.EncodeToString(digest[:])
}
```

- [ ] **Step 5: Persist worker generations and complete checkpoints transactionally**

`WORKER_GROUP_STARTED` increments `worker_restart_count` only when generation increases.
`CHECKPOINT_COMPLETE` validates that `object_path` stays below the submitting user’s resolved
output prefix and inserts with `complete=true`. Replayed event IDs return the existing result.

- [ ] **Step 6: Run security and repository tests**

Run: `cd backend && go test ./domain ./repositories ./api ./k8s -run 'TrainEvent|Checkpoint|Token' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit event and checkpoint persistence**

```bash
git add backend/db/migrations/0022_training_checkpoints.up.sql backend/domain/training_checkpoint.go backend/domain/training_checkpoint_test.go backend/repositories/training_checkpoints.go backend/repositories/training_checkpoints_test.go backend/api/job_train_events.go backend/api/job_train_events_test.go backend/api/job_checkpoints.go backend/api/job_checkpoints_test.go backend/main.go backend/k8s/secrets.go backend/k8s/secrets_test.go backend/k8s/rayjob.go
git commit -m "feat: persist managed training checkpoints"
```

### Task 12: Add `RECOVERING` and outer RayCluster attempts

**Files:**
- Modify: `backend/domain/job.go`
- Modify: `backend/domain/job_test.go`
- Modify: `backend/domain/training_job.go`
- Modify: `backend/repositories/jobs.go`
- Modify: `backend/repositories/jobs_test.go`
- Modify: `backend/k8s/reconciler.go`
- Modify: `backend/k8s/reconciler_test.go`
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_lifecycle_test.go`

- [ ] **Step 1: Write failing state-machine and retry tests**

```go
func TestRunningManagedJobCanEnterRecovering(t *testing.T) {
	if !CanTransition(StateRunning, StateRecovering) || !CanTransition(StateRecovering, StateRunning) {
		t.Fatal("managed recovery transitions are missing")
	}
}

func TestFailedManagedClusterCreatesNextAttemptFromCheckpoint(t *testing.T) {
	job := managedRunningJob()
	job.ClusterAttempt = 1
	job.ResumeCheckpointID = "ckpt-epoch-4"
	reconciler := newRecoveryFixture(job)
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if got := reconciler.createdRayJobName; got != job.ID+"-a2" {
		t.Fatalf("unexpected retry name %q", got)
	}
}
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd backend && go test ./domain ./repositories ./k8s -run 'Recovering|NextAttempt' -count=1`

Expected: FAIL because `RECOVERING` and attempt creation do not exist.

- [ ] **Step 3: Extend the state machine without changing terminal old jobs**

```go
const StateRecovering State = "RECOVERING"
```

Allow `RUNNING → RECOVERING → RUNNING|FAILED|CANCELED|TIMED_OUT`. A Ray DDP job never enters
`RECOVERING`; it retains current retry and terminal behavior.

- [ ] **Step 4: Implement bounded outer attempts**

When a managed RayJob reaches FAILED because Driver, Head, or whole-cluster infrastructure failed:

1. require a complete checkpoint;
2. require `cluster_attempt <= max_failures`;
3. atomically increment `cluster_attempt`, set `RECOVERING`, and snapshot checkpoint ID;
4. render a new RayJob name `<job-id>-a<attempt>`;
5. preserve the platform job ID, MLflow provenance, tenant, output prefix, and Kueue queue;
6. never retry user cancellation, invalid code, deterministic configuration failure, OOM, or NaN.

Classify only explicit infrastructure reasons as recoverable. Unknown failures remain FAILED.

- [ ] **Step 5: Protect recovery against duplicate reconcilers**

Use a repository transaction with `SELECT ... FOR UPDATE` and compare-and-swap on
`cluster_attempt`. Ensure two backend replicas cannot create attempts 2 and 3 concurrently.

- [ ] **Step 6: Run domain, repository, and reconciler tests**

Run: `cd backend && go test ./domain ./repositories ./k8s -count=1`

Expected: PASS.

- [ ] **Step 7: Commit managed recovery**

```bash
git add backend/domain/job.go backend/domain/job_test.go backend/domain/training_job.go backend/repositories/jobs.go backend/repositories/jobs_test.go backend/k8s/reconciler.go backend/k8s/reconciler_test.go backend/k8s/rayjob.go backend/k8s/rayjob_lifecycle_test.go
git commit -m "feat: recover managed ray train attempts"
```

## Release 3 — Data modes and managed observability

### Task 13: Make existing stable paths authoritative and add a Ray Data pilot

**Files:**
- Modify: `backend/domain/training_engine.go`
- Modify: `backend/domain/training_engine_test.go`
- Modify: `backend/k8s/rayjob.go`
- Create: `backend/k8s/rayjob_data_modes_test.go`
- Modify: `images/workspace/raytrain_runtime/managed_driver.py`
- Create: `images/workspace/raytrain_runtime/ray_data.py`
- Create: `images/workspace/raytrain_runtime/test_ray_data.py`
- Create: `examples/ray-data/train_image_batches.py`
- Create: `examples/ray-data/test_train_image_batches.py`

- [ ] **Step 1: Write failing mount/cache/Ray Data contract tests**

```go
func TestEveryDataModeKeepsStableContainerPaths(t *testing.T) {
	for _, mode := range []domain.DataMode{domain.DataModeMount, domain.DataModeCache, domain.DataModeRayData} {
		job := managedJobWithDataMode(mode)
		resource := mustRender(t, job)
		env := workerEnvironment(t, resource)
		if env["PLATFORM_OUTPUT_PATH"] != domain.DataMountOutputPath {
			t.Fatalf("mode %s changed output path", mode)
		}
	}
}
```

Python tests must prove dataset shards are obtained with `ray.train.get_dataset_shard()` and
that mount/cache mode never imports Ray Data.

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd backend && go test ./domain ./k8s -run 'DataMode|StableContainerPaths' -count=1`

Run: `python3 -m unittest images.workspace.raytrain_runtime.test_ray_data -v`

Expected: FAIL because the Ray Data adapter is absent.

- [ ] **Step 3: Preserve existing path and cache behavior**

Map modes as follows:

- `mount`: existing governed input remains `/mnt/data/input`;
- `cache`: require existing `cache.mode=runtime` and `cache.preload=input`; the preloader changes
  `PLATFORM_DATASET_PATH` to the node-local view but leaves output/checkpoint persistent;
- `ray-data`: mount the source read-only, enable dual-NVMe object spilling, and pass a named Ray
  Dataset into `TorchTrainer`.

Do not remove `/mnt/storage/public`, `/mnt/storage/me`, or `/mnt/storage/team` roots.

- [ ] **Step 4: Implement an opt-in Ray Data adapter**

```python
def build_dataset(config):
    if config.format == "parquet":
        return ray.data.read_parquet(config.uri)
    if config.format == "images":
        return ray.data.read_images(config.uri, include_paths=True)
    raise ValueError(f"unsupported Ray Data format: {config.format}")

def worker_iterator(name="train"):
    shard = ray.train.get_dataset_shard(name)
    if shard is None:
        raise RuntimeError(f"Ray Data shard {name!r} is unavailable")
    return shard.iter_torch_batches(prefetch_batches=2)
```

The first pilot supports registered Parquet/image-shard datasets only. It does not attempt to
reinterpret BEVFusion’s arbitrary PKL and small-file loader.

- [ ] **Step 5: Run data-mode and example tests**

Run: `cd backend && go test ./domain ./k8s -run 'DataMode|Cache|StableContainerPaths' -count=1`

Run: `python3 -m unittest discover -s images/workspace/raytrain_runtime -p 'test_*.py' -v`

Run: `python3 -m unittest discover -s examples/ray-data -p 'test_*.py' -v`

Expected: PASS.

- [ ] **Step 6: Commit the data-mode implementation**

```bash
git add backend/domain/training_engine.go backend/domain/training_engine_test.go backend/k8s/rayjob.go backend/k8s/rayjob_data_modes_test.go images/workspace/raytrain_runtime examples/ray-data
git commit -m "feat: add managed ray data mode"
```

### Task 14: Expose worker, recovery, CPU, memory, NCCL, and data performance

**Files:**
- Create: `backend/domain/training_performance.go`
- Create: `backend/observability/training_performance.go`
- Create: `backend/observability/training_performance_test.go`
- Create: `backend/api/job_training_performance.go`
- Create: `backend/api/job_training_performance_test.go`
- Modify: `backend/api/jobs_scoped_routes.go`
- Create: `frontend/src/api/jobTrainingPerformance.js`
- Create: `frontend/src/jobTrainingPerformance.js`
- Create: `frontend/src/jobTrainingPerformance.test.js`
- Create: `frontend/src/components/job/WorkerTable.vue`
- Create: `frontend/src/components/job/DataPerformancePanel.vue`
- Create: `frontend/src/components/job/RecoveryTimeline.vue`
- Modify: `frontend/src/views/Job/JobDetail.vue`

- [ ] **Step 1: Write failing task-scoping and diagnosis tests**

```go
func TestTrainingPerformanceQueriesAreScopedToThePersistedJob(t *testing.T) {
	client := fakePrometheusRecordingQueries()
	_, err := client.QueryTrainingPerformance(ctx, domain.TrainingWorkloadRef{
		Namespace: "tenant-a", RayClusterName: "job-a-cluster",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range client.Queries {
		if !strings.Contains(query, `exported_namespace="tenant-a"`) || !strings.Contains(query, `ray_io_cluster="job-a-cluster"`) {
			t.Fatalf("unscoped query: %s", query)
		}
	}
}
```

Frontend tests must prove that high `data_time/step_time` produces a data bottleneck hint, high
NCCL time produces a communication hint, and missing metrics render “暂无数据” rather than zero.

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd backend && go test ./observability ./api -run 'TrainingPerformance' -count=1`

Run: `cd frontend && npm test -- src/jobTrainingPerformance.test.js`

Expected: FAIL because the endpoint and UI model are absent.

- [ ] **Step 3: Implement scoped Prometheus queries**

Return immutable series for:

- container CPU cores and memory working set per Ray Worker Pod;
- node network transmit/receive rate;
- existing DCGM GPU utilization, memory, power, and temperature;
- Ray object-store bytes and spill throughput;
- cache bytes, hit/miss and preloader duration from the existing cache monitor;
- training `step_time`, `data_time`, and NCCL duration when reported by the managed hook.

All selectors derive from the persisted namespace and RayCluster name; reject unsafe label values.

- [ ] **Step 4: Add deterministic diagnosis rules**

```js
export const diagnosePerformance = (summary) => {
  const dataRatio = summary.stepTime > 0 ? summary.dataTime / summary.stepTime : null
  if (dataRatio !== null && dataRatio >= 0.2) return { code: 'DATA_BOUND', severity: 'warning' }
  if (summary.ncclRatio !== null && summary.ncclRatio >= 0.2) return { code: 'COMMUNICATION_BOUND', severity: 'warning' }
  if (summary.gpuUtilization !== null && summary.gpuUtilization < 50) return { code: 'GPU_UNDERUTILIZED', severity: 'info' }
  return { code: 'BALANCED', severity: 'success' }
}
```

Rules produce advice only and never mutate training parameters.

- [ ] **Step 5: Add Worker, data, and recovery panels**

The task page shows Rank, node, GPU, state, step, GPU utilization, data wait, restart count,
cluster attempt, resume checkpoint, CPU, memory, and network. Preserve the existing Loki log,
MLflow, GPU trend, and Ray Dashboard tabs.

- [ ] **Step 6: Run backend/frontend suites and build**

Run: `cd backend && go test ./observability ./api -count=1`

Run: `cd frontend && npm test && npm run build`

Expected: PASS.

- [ ] **Step 7: Commit managed observability**

```bash
git add backend/domain/training_performance.go backend/observability/training_performance.go backend/observability/training_performance_test.go backend/api/job_training_performance.go backend/api/job_training_performance_test.go backend/api/jobs_scoped_routes.go frontend/src
git commit -m "feat: expose managed training performance"
```

## Release 4 — KubeRay upgrade and production acceptance

### Task 15: Add Helm feature gates and guarded KubeRay 1.6.2 operations

**Files:**
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/values-prod.yaml.example`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `scripts/test-delivery-render.sh`
- Create: `ops/kuberay/preflight-upgrade.sh`
- Create: `ops/kuberay/backup.sh`
- Create: `ops/kuberay/upgrade-1.6.2.sh`
- Create: `ops/kuberay/verify.sh`
- Create: `ops/kuberay/test/upgrade-contract-test.sh`

- [ ] **Step 1: Write failing Helm and shell contract tests**

The tests assert:

```bash
grep -F 'RAY_TRAIN_MANAGED_ENABLED' "$rendered"
grep -F 'RAY_TRAIN_CANARY_ENABLED' "$rendered"
grep -F 'RAY_TRAIN_CANARY_TENANTS' "$rendered"
grep -F 'kubectl get rayjobs.ray.io --all-namespaces' ops/kuberay/preflight-upgrade.sh
grep -F 'kubectl replace -k' ops/kuberay/upgrade-1.6.2.sh
grep -F 'helm upgrade kuberay-operator' ops/kuberay/upgrade-1.6.2.sh
```

The preflight must fail when any non-terminal RayJob, running RayCluster, or running debug
workspace exists.

- [ ] **Step 2: Run contract tests and confirm RED**

Run: `bash scripts/test-delivery-render.sh && bash ops/kuberay/test/upgrade-contract-test.sh`

Expected: FAIL because feature gates and guarded scripts are absent.

- [ ] **Step 3: Render disabled-by-default feature gates**

```yaml
rayTrain:
  managedEnabled: false
  canaryEnabled: false
  canaryTenants: []
  productionVersion: "2.56.1"
  canaryVersion: "2.58.0"
```

The backend Deployment receives matching environment variables, including a comma-separated
`RAY_TRAIN_CANARY_TENANTS` rendered from `rayTrain.canaryTenants`. Changing them affects only new
submissions; the reconciler uses each persisted job’s immutable snapshot.

- [ ] **Step 4: Implement a fail-closed operator upgrade workflow**

`preflight-upgrade.sh` verifies zero active jobs/workspaces, Kueue health, CRD version, two ready
operator replicas, and API access. `backup.sh` stores CRDs, operator Deployment, Helm values, and
Ray/Kueue resources in a timestamped directory supplied by the caller.

`upgrade-1.6.2.sh` runs only after a successful preflight and explicit
`CONFIRM_KUBERAY_UPGRADE=1`. It upgrades CRDs first and Operator second, then invokes `verify.sh`.
It never changes Ray runtime images or existing tenant resources.

- [ ] **Step 5: Run render and operations contract tests**

Run: `bash scripts/test-delivery-render.sh`

Run: `bash ops/kuberay/test/upgrade-contract-test.sh`

Expected: PASS.

- [ ] **Step 6: Commit deployment gates and runbook scripts**

```bash
git add helm/ray-train-platform scripts/test-delivery-render.sh ops/kuberay
git commit -m "ops: guard kuberay 1.6 upgrade"
```

### Task 16: Build automated acceptance and fault-injection harnesses

**Files:**
- Create: `scripts/e2e-ray-runtime-upgrade.sh`
- Create: `scripts/e2e-ray-train-managed.sh`
- Create: `scripts/e2e-ray-train-faults.sh`
- Create: `scripts/test-ray-train-e2e-contract.sh`
- Modify: `scripts/e2e-training.sh`
- Modify: `examples/distributed-demo/ray_train_smoke.py`
- Create: `examples/distributed-demo/ray_train_checkpoint_smoke.py`
- Create: `examples/distributed-demo/ray_train_checkpoint_smoke_test.py`

- [ ] **Step 1: Write failing harness contract tests**

```bash
grep -F '1gpu' scripts/e2e-ray-train-managed.sh
grep -F '8gpu' scripts/e2e-ray-train-managed.sh
grep -F '2x8gpu' scripts/e2e-ray-train-managed.sh
grep -F 'ALLOW_DESTRUCTIVE_FAULT_TESTS' scripts/e2e-ray-train-faults.sh
grep -F 'spk-rayjob' scripts/e2e-ray-runtime-upgrade.sh
grep -F 'native-ray' scripts/e2e-ray-runtime-upgrade.sh
grep -F 'portal' scripts/e2e-ray-runtime-upgrade.sh
```

- [ ] **Step 2: Run the contract test and confirm RED**

Run: `bash scripts/test-ray-train-e2e-contract.sh`

Expected: FAIL because the harnesses do not exist.

- [ ] **Step 3: Implement non-destructive acceptance first**

The runtime harness submits Ray DDP jobs on Ray 2.56.1 at 1 GPU, 1×8, and 2×8 through Portal,
`spk-rayjob`, and native Ray API. The managed harness repeats the matrix and verifies:

- 16 Train workers for 2×8;
- logs, MLflow and Ray Dashboard access;
- complete checkpoint manifest;
- a child job resumes from that checkpoint;
- no Service, PVC, RayCluster, GPU allocation, or Kueue workload remains after TTL.

Every created resource carries a unique acceptance prefix and is recorded in a cleanup ledger.

- [ ] **Step 4: Gate destructive faults behind explicit authorization**

The fault script exits unless `ALLOW_DESTRUCTIVE_FAULT_TESTS=1` and a dedicated acceptance job ID
are both set. It only targets resources labelled with that job ID. Cover Worker process exit,
Worker Pod deletion, controlled DNS/TOS transient injection, GPU node restart, and Driver/Head
failure. It must refuse broad selectors and refuse a node fault when unrelated GPU workloads run
on that node.

- [ ] **Step 5: Run local harness tests**

Run: `python3 -m unittest discover -s examples/distributed-demo -p 'ray_train_checkpoint_smoke_test.py' -v`

Run: `bash scripts/test-ray-train-e2e-contract.sh`

Expected: PASS without accessing a cluster.

- [ ] **Step 6: Commit acceptance tooling**

```bash
git add scripts examples/distributed-demo
git commit -m "test: add ray train acceptance harness"
```

### Task 17: Update delivery, user, and operations documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/OPERATIONS_GUIDE.md`
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/SUBMIT_GUIDE.md`
- Modify: `docs/NVME_CACHE_GUIDE.md`
- Modify: `docs/BEVFUSION_END_TO_END_GUIDE.md`
- Modify: `docs/PLATFORM_ROADMAP.md`
- Create: `docs/RAY_TRAIN_MANAGED_GUIDE.md`
- Modify: `scripts/test_docs.py`

- [ ] **Step 1: Add failing documentation contract tests**

```python
def test_managed_guide_names_exact_version_and_rollback(self):
    text = read("docs/RAY_TRAIN_MANAGED_GUIDE.md")
    self.assertIn("Ray 2.56.1", text)
    self.assertIn("KubeRay 1.6.2", text)
    self.assertIn("--engine ray-train", text)
    self.assertIn("--engine ray-ddp", text)
    self.assertIn("RAY_TRAIN_MANAGED_ENABLED", text)

def test_existing_jobs_are_explicitly_immutable(self):
    text = read("docs/OPERATIONS_GUIDE.md")
    self.assertIn("不得修改运行中 RayJob", text)
```

- [ ] **Step 2: Run docs tests and confirm RED**

Run: `python3 scripts/test_docs.py`

Expected: FAIL because the managed guide and contracts are absent.

- [ ] **Step 3: Document exact user workflows**

`docs/RAY_TRAIN_MANAGED_GUIDE.md` contains complete Portal, CLI, and native API examples; code
upload behavior; supported Python entrypoints; MMCV adapter code; checkpoint/resume; data modes;
Ray Dashboard; MLflow; logs; performance diagnosis; and failure meanings. It states that code
changes do not rebuild images.

- [ ] **Step 4: Document operations and rollback**

The operations guide includes feature gates, side-by-side runtime images, zero-workload KubeRay
upgrade, backups, verification, rollback, known Ray 2.35 risk, History Server alpha status, and
the rule that operator upgrades never run during training.

- [ ] **Step 5: Run docs and link tests**

Run: `python3 scripts/test_docs.py`

Expected: PASS with no unresolved relative links.

- [ ] **Step 6: Commit documentation**

```bash
git add README.md docs scripts/test_docs.py
git commit -m "docs: deliver ray train managed operations"
```

### Task 18: Execute staged production validation and switch defaults

**Files:**
- Modify after evidence is collected: `deploy/profiles/vke-cpu-ha.yaml`
- Modify after evidence is collected: `helm/ray-train-platform/values-prod.yaml.example`
- Create: `docs/reports/RAY_TRAIN_256_ACCEPTANCE.md`
- Create: `docs/reports/RAY_TRAIN_258_CANARY.md`

- [ ] **Step 1: Run the complete local quality gate**

Run:

```bash
cd backend && go test ./... -count=1
cd ../frontend && npm test && npm run build
cd .. && python3 -m unittest discover -s images/workspace/raytrain_runtime -p 'test_*.py' -v
python3 -m unittest discover -s examples -p '*_test.py' -v
bash scripts/test-delivery-render.sh
bash scripts/test-ray-runtime-images.sh --contract-only
bash scripts/test-ray-train-e2e-contract.sh
python3 scripts/test_docs.py
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 2: Build and publish immutable Ray 2.56.1 images**

Use the repository build script with the production registry configured through environment
variables. Capture the returned digests, verify them with `scripts/test-ray-runtime-images.sh`,
and register them in the shared image catalog with Ray version 2.56.1 and exact supported
engines. Do not overwrite or delete Ray 2.35 catalog entries.

- [ ] **Step 3: Deploy foundation with managed mode disabled**

Deploy additive migrations, backend, frontend, and CLI with:

```yaml
rayTrain:
  managedEnabled: false
  canaryEnabled: false
```

Verify currently running RayJob UIDs, Pod UIDs, image IDs, and resource versions are unchanged.
Submit the Ray DDP acceptance matrix on Ray 2.56.1 and compare against the recorded 2.35 baseline;
step-time regression must be no more than 5% under identical inputs.

- [ ] **Step 4: Enable managed mode for the acceptance tenant only**

Use a tenant-scoped capability record rather than a global flag to run 1 GPU, 1×8, and 2×8.
Complete checkpoint resume and authorized fault injection. Record job IDs, code SHA, image digest,
dataset version, timing, loss, mAP/NDS, restart counts, and cleanup evidence in
`docs/reports/RAY_TRAIN_256_ACCEPTANCE.md`.

- [ ] **Step 5: Upgrade KubeRay in the zero-workload window**

After the platform confirms no running RayJob or debug workspace, run `ops/kuberay/backup.sh`,
`ops/kuberay/preflight-upgrade.sh`, and the explicitly confirmed 1.6.2 upgrade. Re-run 1 GPU and
2×8 managed acceptance before reopening general submissions.

- [ ] **Step 6: Switch new-job defaults only after all gates pass**

Set the default shared training image to the Ray 2.56.1 DDP-compatible runtime, enable managed
selection globally, and prevent new Ray 2.35 submissions in `runtimecatalog.Policy`. Keep
`ray-ddp` as the form default until the approved BEVFusion branches also pass managed mode.

- [ ] **Step 7: Run Ray 2.58.0 as canary, not production default**

Repeat the same matrix and record evidence in `docs/reports/RAY_TRAIN_258_CANARY.md`. Do not change
the production default in this task.

- [ ] **Step 8: Perform and record a rollback drill**

Disable managed submission, select the Ray 2.56.1 DDP runtime, and disable Ray Data while leaving
an existing managed job untouched. Verify new jobs use the fallback and previous Checkpoints,
MLflow Runs, Loki logs, and database fields remain available.

- [ ] **Step 9: Commit evidence and production profiles**

```bash
git add deploy/profiles/vke-cpu-ha.yaml helm/ray-train-platform/values-prod.yaml.example docs/reports/RAY_TRAIN_256_ACCEPTANCE.md docs/reports/RAY_TRAIN_258_CANARY.md
git commit -m "chore: promote ray 2.56 managed runtime"
```

## Final completion gate

Do not call this implementation complete until all of the following are true:

- Existing Ray 2.35 jobs completed without mutation or interruption.
- Ray 2.56.1 Ray DDP passed 1 GPU, 1×8, and 2×8 through all three submission paths.
- Both approved BEVFusion branches passed Ray DDP on Ray 2.56.1.
- Both approved BEVFusion branches passed managed Ray Train or have a documented, approved
  compatibility exception that keeps them on Ray DDP.
- Managed Worker, Pod, node, Driver, and Head recovery was demonstrated with complete checkpoints.
- KubeRay 1.6.2 was upgraded only in a zero-workload window and verified after the change.
- No user can access another tenant’s jobs, events, Checkpoints, Dashboard, or metrics.
- No lingering GPU allocation, RayCluster, Service, PVC, Kueue workload, or callback Secret remains.
- Ray 2.58.0 remains canary until its separate report is approved.
