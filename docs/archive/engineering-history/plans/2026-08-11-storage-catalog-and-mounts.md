# Storage Catalog and Mounts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace user-entered data URIs with authorized TOS/IDC storage-directory selections and mount those selections into Ray training Pods without injecting TOS credentials into the workload.

**Architecture:** A tenant-scoped storage-asset catalogue maps an authorized TOS prefix or IDC root to an infrastructure-owned PVC. The backend authorizes directory browsing and resolves the user’s asset selections during submission. The RayJob renderer mounts only resolved PVCs at fixed local paths. Existing TOS credentials remain limited to the platform and code-materializer init container.

**Tech Stack:** Go 1.25, Gin, GORM/PostgreSQL, Volcengine TOS Go SDK v2, Kubernetes RayJob unstructured manifests, Vue 3, Element Plus.

---

### Task 1: Persist and validate storage assets

**Files:**
- Create: `backend/db/migrations/0006_storage_assets.up.sql`
- Create: `backend/domain/storage_asset.go`
- Create: `backend/domain/storage_asset_test.go`
- Create: `backend/repositories/storage_assets.go`
- Create: `backend/repositories/storage_assets_test.go`

- [ ] **Step 1: Write failing domain tests**

```go
func TestStorageAssetValidateRejectsUnsafePrefixAndWritableInput(t *testing.T) {
    asset := domain.StorageAsset{ID: "asset-1", TenantID: "local", Name: "bad", Kind: domain.StorageAssetDataset, Provider: domain.StorageProviderTOS, ClaimName: "tos-data", RootPrefix: "datasets/../private", ReadOnly: false}
    if err := asset.Validate(); err == nil { t.Fatal("unsafe asset was accepted") }
}

func TestResolveStorageSelectionKeepsPathInsideAssetRoot(t *testing.T) {
    asset := validDatasetAsset()
    mount, err := asset.Resolve("images/v1")
    if err != nil || mount.RelativePath != "images/v1" { t.Fatalf("resolve: %#v %v", mount, err) }
    if _, err := asset.Resolve("../secret"); err == nil { t.Fatal("traversal accepted") }
}
```

- [ ] **Step 2: Run domain tests and verify RED**

Run: `cd backend && go test ./domain -run 'TestStorageAsset'`

Expected: compile failure because `StorageAsset` does not exist.

- [ ] **Step 3: Create the migration and domain model**

Create table fields: `id`, nullable `tenant_id`, nullable `owner_user_id`, `name`, `description`, `kind`, `provider`, `claim_name`, `root_prefix`, `read_only`, `browse_enabled`, `created_by`, timestamps. Add an index on `(tenant_id, kind)` and an owner index. Implement `StorageAsset.Validate`, `StorageAsset.AllowedFor`, `StorageAsset.Resolve`, canonical relative-path validation, and the three fixed mount paths.

- [ ] **Step 4: Implement repository CRUD and visibility queries**

Implement `CreateStorageAsset`, `ListStorageAssets`, `GetStorageAsset`, and `DeleteStorageAsset`. `ListStorageAssets` must return shared assets, tenant assets, and assets owned by the requesting user; it must not return another user’s private assets. Model nullable tenant and owner fields exactly as `PlatformImageRecord` does.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run: `cd backend && go test ./domain ./repositories -run 'Test(StorageAsset|ResolveStorage)'`

Expected: `ok` for both packages.

### Task 2: Add constrained TOS directory listing

**Files:**
- Modify: `backend/objectstore/store.go`
- Modify: `backend/objectstore/tos.go`
- Modify: `backend/objectstore/tos_store.go`
- Create: `backend/objectstore/tos_list_test.go`

- [ ] **Step 1: Write failing list-boundary tests**

```go
func TestTOSStoreListDirectoriesUsesDelimiterAndNeverEscapesRoot(t *testing.T) {
    store := newFakeStoreWithPrefixes("datasets/local/a/", "datasets/local/b/")
    page, err := store.ListDirectories(context.Background(), "datasets/local/a/", "", 50)
    if err != nil || len(page.Directories) != 1 { t.Fatalf("page=%#v err=%v", page, err) }
    if _, err := store.ListDirectories(context.Background(), "datasets/local/a/../", "", 50); err == nil { t.Fatal("traversal accepted") }
}
```

- [ ] **Step 2: Run the object-store test and verify RED**

Run: `cd backend && go test ./objectstore -run TestTOSStoreListDirectories`

Expected: compile failure because `ListDirectories` is missing.

- [ ] **Step 3: Implement a minimal directory-listing interface**

Add `DirectoryPage` and `DirectoryLister`; call TOS `ListObjectsType2` with `Delimiter: "/"`, `MaxKeys <= 100`, and an opaque continuation token. Return only direct child directory names and the next token. Validate the root/prefix before the SDK call and map SDK errors to `ErrUnavailable`; do not return bucket, key, SDK error, or signed URL.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `cd backend && go test ./objectstore -run 'TestTOSStore(ListDirectories|Head)'`

Expected: `ok`.

### Task 3: Expose authorized catalogue and directory APIs

**Files:**
- Create: `backend/api/storage_assets.go`
- Create: `backend/api/storage_assets_test.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/main.go`

- [ ] **Step 1: Write failing HTTP tests**

```go
func TestStorageAssetListHidesOtherTenantAssets(t *testing.T) {
    router := storageAssetRouter(t, engineer("local"))
    response := httptest.NewRecorder()
    router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-assets?kind=dataset", nil))
    if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "tenant-b") { t.Fatalf("status=%d body=%s", response.Code, response.Body.String()) }
}

func TestStorageAssetDirectoryRouteRejectsTraversal(t *testing.T) {
    router := storageAssetRouter(t, engineer("local"))
    response := httptest.NewRecorder()
    router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-assets/asset-1/directories?path=../secret", nil))
    if response.Code != http.StatusBadRequest { t.Fatalf("status=%d", response.Code) }
}
```

- [ ] **Step 2: Run HTTP tests and verify RED**

Run: `cd backend && go test ./api -run TestStorageAsset`

Expected: compile failure because routes and the storage store are missing.

- [ ] **Step 3: Implement user and admin routes**

Register `GET /storage-assets`, `GET /storage-assets/:id/directories`, `POST /storage-assets`, and `DELETE /storage-assets/:id`. List/browse requires an interactive authenticated principal. Create/delete requires `TenantAdmin`; `shared=true` requires `SuperAdmin`. Directory browsing checks asset visibility and `provider == tos` before accessing a `DirectoryLister`; missing lister returns `503 STORAGE_BROWSER_UNAVAILABLE`.

- [ ] **Step 4: Wire repository and TOS store from main**

Pass the repository as `StorageAssets` and the initialized TOS store as `DirectoryLister` only when TOS configuration is valid. Do not add TOS credentials to API responses, frontend runtime configuration, or logs.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run: `cd backend && go test ./api -run TestStorageAsset`

Expected: `ok`.

### Task 4: Resolve selected storage assets during job submission

**Files:**
- Modify: `backend/domain/job.go`
- Modify: `backend/domain/data_location.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/api/submission_service_test.go`
- Modify: `backend/api/submission_image_catalog_test.go`

- [ ] **Step 1: Write failing submission tests**

```go
func TestSubmissionResolvesOnlyVisibleStorageAssets(t *testing.T) {
    service := submitWithStorageAssets(t, []domain.StorageAsset{validDatasetAsset()})
    _, err := service.Submit(context.Background(), SubmissionInput{Principal: engineer("local"), Spec: specSelecting("asset-foreign")})
    if !errors.Is(err, ErrSubmissionStorageAssetNotAllowed) { t.Fatalf("err=%v", err) }
}

func TestSubmissionGeneratesIsolatedOutputDirectory(t *testing.T) {
    service := submitWithStorageAssets(t, []domain.StorageAsset{validOutputAsset()})
    job, err := service.Submit(context.Background(), SubmissionInput{Principal: engineer("local"), Spec: specSelectingOutput("asset-output")})
    if err != nil || !strings.HasPrefix(job.Spec.ResolvedStorage.Output.RelativePath, "runs/"+job.ID) { t.Fatalf("job=%#v err=%v", job, err) }
}
```

- [ ] **Step 2: Run submission tests and verify RED**

Run: `cd backend && go test ./api -run 'TestSubmission(ResolvesOnlyVisibleStorageAssets|GeneratesIsolatedOutputDirectory)'`

Expected: compile failure because storage selections do not exist.

- [ ] **Step 3: Add request selections and resolved storage mounts**

Add `StorageSelection` (`assetId`, `relativePath`) for dataset/checkpoint/output and `ResolvedStorageMounts` to `JobSpec`. Keep legacy URI fields readable for existing API clients, but reject a request that combines URI and asset selection. Clear any client-supplied resolved mounts before resolution.

- [ ] **Step 4: Resolve selections in SubmissionService**

Add `StorageAssetStore` to `SubmissionServiceOptions`. Resolve every selected ID against principal tenant/user visibility; require matching asset kind; require read-only input assets; require writable output asset; create output `runs/<job-id>` only after ID allocation. Return stable errors `STORAGE_ASSET_NOT_ALLOWED`, `STORAGE_ASSET_KIND_INVALID`, and `STORAGE_OUTPUT_NOT_WRITABLE` through the HTTP error mapper.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run: `cd backend && go test ./api ./domain -run 'TestSubmission.*Storage|TestJobSpec.*Storage'`

Expected: `ok`.

### Task 5: Mount resolved PVCs into Ray Head and Workers

**Files:**
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_test.go`
- Create: `backend/k8s/rayjob_storage_mounts_test.go`

- [ ] **Step 1: Write failing renderer tests**

```go
func TestRenderRayJobMountsResolvedDatasetAndOutputWithoutWorkloadCredentials(t *testing.T) {
    job := validRenderJob()
    job.Spec.ResolvedStorage = resolvedDatasetAndOutput()
    manifest, err := RenderRayJob(job, testRenderOptions())
    if err != nil { t.Fatal(err) }
    worker := workerContainer(t, manifest)
    assertMount(t, worker, "/mnt/data/dataset", "tos-data-ro", true)
    assertMount(t, worker, "/mnt/data/output", "tos-output-rw", false)
    assertNoEnv(t, worker, "AWS_ACCESS_KEY_ID")
}
```

- [ ] **Step 2: Run renderer tests and verify RED**

Run: `cd backend && go test ./k8s -run TestRenderRayJobMountsResolved`

Expected: failure because resolved mounts are not rendered and credentials are still injected.

- [ ] **Step 3: Render fixed data mounts**

Attach resolved dataset/checkpoint/output PVCs only to Ray Head and Worker templates. Use volume names derived from role, mount to the fixed `/mnt/data/*` paths, set input mounts/volumes read-only, and append local path environment variables. Do not attach data volumes to the submitter template. Remove the global TOS credential environment injection from primary Ray containers; leave credential use confined to the fixed source-materializer init container.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `cd backend && go test ./k8s -run 'TestRenderRayJob.*(Storage|Data|Mount)'`

Expected: `ok`.

### Task 6: Replace URI fields with storage pickers in Portal

**Files:**
- Create: `frontend/src/api/storageAssets.js`
- Create: `frontend/src/api/storageAssets.test.js`
- Create: `frontend/src/components/StorageDirectoryPicker.vue`
- Create: `frontend/src/components/StorageDirectoryPicker.test.js`
- Modify: `frontend/src/views/Job/CreateJob.vue`
- Modify: `frontend/src/views/DataCache/index.vue`
- Modify: `frontend/src/submission.js`
- Modify: `frontend/src/submission.test.js`

- [ ] **Step 1: Write failing selection-builder tests**

```js
it('submits storage asset selections instead of editable URIs', () => {
  const spec = buildJobSpec({ ...validForm, datasetStorage: { assetId: 'dataset-a', relativePath: 'train' }, checkpointStorage: {}, outputStorage: { assetId: 'output-a' } })
  assert.deepEqual(spec.datasetStorage, { assetId: 'dataset-a', relativePath: 'train' })
  assert.equal(spec.datasetUri, '')
})
```

- [ ] **Step 2: Run frontend test and verify RED**

Run: `cd frontend && node --test src/submission.test.js`

Expected: failure because asset selections are not serialized.

- [ ] **Step 3: Implement API client and picker**

Implement list and directory API wrappers. The picker lists only backend-returned assets, resolves folders lazily with a breadcrumb and pagination cursor, never renders bucket name or a free URI field, and allows the selected asset root when no child directory is required.

- [ ] **Step 4: Replace the training form data step**

Replace the three URI inputs with dataset/checkpoint/output pickers. Output picker displays the generated `runs/<job-id>` behavior. Update `DataCache` into a catalogue page showing current user assets and a clear empty state when operators have not registered PVC-backed storage roots.

- [ ] **Step 5: Run frontend tests and build**

Run: `cd frontend && node --test src/submission.test.js src/api/storageAssets.test.js && npm run build`

Expected: all tests pass and Vite exits `0`.

### Task 7: Document provisioning and validate end-to-end

**Files:**
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/CLUSTER_DEPLOYMENT_GUIDE.md`
- Modify: `docs/USER_GUIDE.md`

- [ ] **Step 1: Document operator-owned storage prerequisites**

Document a FSX TOS static PV/PVC per minimal TOS prefix and a read-only NFS PV/PVC per IDC export. State the exact validation commands: `kubectl get csidriver fsx.csi.volcengine.com`, `kubectl -n tenant-<id> get pvc`, and a smoke Pod `mount`/`touch` check. State that workload AWS credentials must not be present.

- [ ] **Step 2: Run full verification**

Run: `cd backend && go test ./...` and `cd frontend && npm run build`.

Expected: both commands exit `0`.

- [ ] **Step 3: Build and deploy the backend and frontend**

Run on the build host: `BUILD_TARGETS=backend,frontend PUSH_IMAGE=true IMAGE_TAG=<immutable-tag> bash build-image.sh`; then `helm upgrade ray-platform ./helm/ray-train-platform -n ray-train-platform --reuse-values --set backend.image.tag=<immutable-tag> --set frontend.image.tag=<immutable-tag> --wait --timeout 10m`.

- [ ] **Step 4: Execute cluster acceptance**

After operators create a real TOS FSX PVC and one IDC NFS PVC, create catalogue assets; sign in as Engineer; select an allowed dataset and output; submit a one-GPU job; verify `/mnt/data/dataset` is read-only, `/mnt/data/output/runs/<job-id>` is writable, no workload AWS variables exist, and the unauthorized root is absent.
