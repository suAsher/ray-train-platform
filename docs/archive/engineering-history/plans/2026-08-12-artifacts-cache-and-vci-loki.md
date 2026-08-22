# 训练产物、节点缓存与 VCI Loki Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide tenant-safe task artifact browsing, a future-proof local GPU cache contract, and durable Loki logging that remains available after the test GPU node is removed.

**Architecture:** Extend the existing storage-asset boundary with job-output-scoped object browsing and short-lived downloads. Configure a three-replica Loki monolithic StatefulSet on VCI with one EBS WAL volume per replica and TOS as the TSDB object store. Keep local GPU disks out of the data trust boundary by modelling them as generic ephemeral cache volumes only after `csi-local` is installed.

**Tech Stack:** Go 1.25, Gin, GORM/PostgreSQL, Volcengine TOS Go SDK v2, Vue 3, Element Plus, Helm 3, Loki Chart 18.7.5, VKE VCI, EBS CSI, FSX CSI.

---

### Task 1: Render and install VCI-resilient Loki

**Files:**
- Create: `ops/observability/loki/20-values-vci-ha.yaml`
- Create: `ops/observability/loki/deploy-vci-ha.sh`
- Create: `ops/observability/loki/30-verify-loki.sh`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/values-prod.yaml.example`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: Write a render assertion script before values**

```bash
helm template loki /opt/guofeng/vke-cluster/loki/loki-18.7.5.tgz \
  -n loki -f ops/observability/loki/20-values-vci-ha.yaml > /tmp/loki-rendered.yaml
grep -q 'storageClassName: ebs-ssd' /tmp/loki-rendered.yaml
grep -q 'node.kubernetes.io/instance-type' /tmp/loki-rendered.yaml
grep -q 'harbor.wellspiking.ai/hub/grafana/loki' /tmp/loki-rendered.yaml
grep -q 'initialize-wal-permissions' /tmp/loki-rendered.yaml
```

- [ ] **Step 2: Run it and verify RED**

Run: `bash ops/observability/loki/deploy-vci-ha.sh --render-only`

Expected: exit non-zero because the production values and script do not exist.

- [ ] **Step 3: Define the Loki values**

Create `20-values-vci-ha.yaml` with these non-secret invariants:

```yaml
deploymentMode: Monolithic
global:
  imageRegistry: harbor.wellspiking.ai/hub
loki:
  storage:
    type: s3
    bucketNames: { chunks: vke-cluster, ruler: vke-cluster, admin: vke-cluster }
    s3:
      endpoint: tos-s3-cn-shanghai.ivolces.com
      region: cn-shanghai
      accessKeyId: ${TOS_ACCESS_KEY_ID}
      secretAccessKey: ${TOS_SECRET_ACCESS_KEY}
  commonConfig: { path_prefix: /var/loki, replication_factor: 3 }
singleBinary:
  replicas: 3
  kind: StatefulSet
  persistence: { enabled: true, storageClass: ebs-ssd, size: 50Gi }
```

Add VCI node affinity, its toleration, `vke.volcengine.com/burst-to-vci: enforce`, zone/hostname `DoNotSchedule` spread, a two-available PDB, 120-second termination grace, and a root init container that only executes `chown 10001:10001 /var/loki && chmod 0770 /var/loki` on the named `storage` volume. Inject `loki-tos` through `defaults.extraEnvFrom`; do not inline credentials.

- [ ] **Step 4: Disable unneeded components and configure gateway**

Set simple-scalable and distributed replica counts to zero, disable MinIO, canary, Helm test, chunks cache, and results cache. Keep a two-replica ClusterIP gateway on VCI with Harbor’s `nginxinc/nginx-unprivileged` image and a PDB of one. Configure TSDB schema v13 from `2026-08-12`, 30-day retention and compactor deletion.

- [ ] **Step 5: Run the render assertion and verify GREEN**

Run: `bash ops/observability/loki/deploy-vci-ha.sh --render-only`

Expected: `Loki render contract verified` with no credential values printed.

- [ ] **Step 6: Deploy after targeted legacy cleanup**

The deploy script must inspect `data-loki-write-{0,1,2}` owner references and require `CLEANUP_LEGACY_PVCS=true` before deleting only Pending, unowned claims. It then runs:

```bash
helm upgrade --install loki /opt/guofeng/vke-cluster/loki/loki-18.7.5.tgz \
  --namespace loki --create-namespace \
  --values ops/observability/loki/20-values-vci-ha.yaml \
  --atomic --timeout 15m
```

- [ ] **Step 7: Point the platform backend at the in-cluster gateway**

Set `backend.lokiURL` to `http://loki-gateway.loki.svc.cluster.local` in the platform chart values and deploy a new Helm revision. Do not expose the gateway through ALB or NodePort.

### Task 2: Verify end-to-end logging and document the storage boundary

**Files:**
- Create: `ops/observability/loki/30-verify-loki.sh`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/ADMIN_GUIDE.md`

- [ ] **Step 1: Write the failing gateway smoke check**

```bash
line="platform-loki-smoke-$(date +%s)"
payload='{"streams":[{"stream":{"app":"ray-platform-smoke","job_id":"loki-smoke"},"values":[["'"$(date +%s%N)"'","'"$line"'"]]}]}'
curl --fail --silent --show-error -X POST http://127.0.0.1:3100/loki/api/v1/push \
  -H 'Content-Type: application/json' --data "$payload"
curl --fail --silent --show-error --get http://127.0.0.1:3100/loki/api/v1/query_range \
  --data-urlencode '{app="ray-platform-smoke",job_id="loki-smoke"}' | grep -q "$line"
```

- [ ] **Step 2: Run it and verify RED before deployment**

Run: `bash ops/observability/loki/30-verify-loki.sh`

Expected: exit non-zero while `loki-gateway` is absent.

- [ ] **Step 3: Implement the verification script**

Start a scoped background `kubectl port-forward` to `svc/loki-gateway`, wait for a TCP response, execute the push/query pair, delete no business data, and kill the port-forward in a shell trap. Then assert three Ready `single-binary` Pods, three Bound `storage-loki-*` PVCs, two Ready gateway Pods, and VCI placement.

- [ ] **Step 4: Run verification and test WAL recovery**

Run: `bash ops/observability/loki/30-verify-loki.sh && kubectl -n loki delete pod loki-0`

Expected: the script prints the unique line, `loki-0` returns Ready on its retained PVC, and `loki-1`/`loki-2` remain Ready.

- [ ] **Step 5: Document operational limits**

Document that TOS is Loki’s object store rather than a PVC; EBS is per-replica WAL; deleting the StatefulSet must retain PVCs; and migrating to full microservices mode is a capacity-driven future change, not a data migration shortcut.

### Task 3: Add tenant-safe task artifact listing and download

**Files:**
- Modify: `backend/objectstore/store.go`
- Modify: `backend/objectstore/tos.go`
- Modify: `backend/objectstore/tos_store.go`
- Create: `backend/objectstore/tos_artifacts_test.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/api/jobs_scoped_routes.go`
- Create: `backend/api/job_artifacts_test.go`
- Create: `frontend/src/api/jobArtifacts.js`
- Create: `frontend/src/api/jobArtifacts.test.js`
- Create: `frontend/src/components/JobArtifactBrowser.vue`
- Modify: `frontend/src/views/Job/JobDetail.vue`

- [ ] **Step 1: Write failing object-scope tests**

```go
func TestTOSStoreListArtifactEntriesNeverEscapesTaskRoot(t *testing.T) {
    store := newFakeArtifactStore("ray-train/tenants/local/outputs/runs/job-a/")
    page, err := store.ListArtifactEntries(context.Background(), "ray-train/tenants/local/outputs/runs/job-a", "checkpoints", "", 50)
    if err != nil || page.Prefix != "" || len(page.Entries) == 0 { t.Fatalf("page=%#v err=%v", page, err) }
    if _, err := store.ListArtifactEntries(context.Background(), "ray-train/tenants/local/outputs/runs/job-a", "../job-b", "", 50); err == nil { t.Fatal("escaped task root") }
}
```

- [ ] **Step 2: Run the scope test and verify RED**

Run: `cd backend && go test ./objectstore -run TestTOSStoreListArtifactEntriesNeverEscapesTaskRoot`

Expected: compile failure because `ListArtifactEntries` is absent.

- [ ] **Step 3: Implement scoped object list and signed GET**

Add `ArtifactEntry` and `ArtifactPage` to `objectstore/store.go`; preserve the internal object key only inside the store. Use TOS ListObjectsV2 with delimiter and page limit 100. Implement `PresignArtifactGet` with a five-minute expiry and safe attachment filename. All joins use `job.Spec.ResolvedStorage.Output.RootPrefix + runs/<job-id>` and canonical relative paths.

- [ ] **Step 4: Write failing HTTP authorization tests**

```go
func TestJobArtifactRouteRejectsOtherTenant(t *testing.T) {
    router := jobArtifactRouter(t, engineer("tenant-a"))
    response := httptest.NewRecorder()
    router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-tenant-b/artifacts", nil))
    if response.Code != http.StatusNotFound { t.Fatalf("status=%d", response.Code) }
}
```

- [ ] **Step 5: Implement API routes**

Register `GET /jobs/:id/artifacts`, `GET /jobs/:id/artifacts/preview`, and `GET /jobs/:id/artifacts/download`. Require `PATScopeJobsRead`, tenant ownership, an output storage selection and a configured object store. Preview only UTF-8 text, JSON and images below 5MiB; return `ARTIFACT_PREVIEW_UNSUPPORTED` for other object types. List metadata excludes object keys, bucket, claim and secret information.

- [ ] **Step 6: Run API tests and verify GREEN**

Run: `cd backend && go test ./api ./objectstore -run 'Test(JobArtifact|TOSStoreListArtifact)'`

Expected: all focused tests pass.

- [ ] **Step 7: Build the artifact browser**

The component must show breadcrumb, directories, size/time, preview and download actions, a clear empty state, and no TOS terminology. It obtains only `/api/v1/jobs/<id>/artifacts*` routes. Add a `训练产物` tab in `JobDetail.vue` and remove the placeholder checkpoint table.

- [ ] **Step 8: Run UI tests and build**

Run: `cd frontend && node --test src/api/jobArtifacts.test.js && npm run build`

Expected: both exit `0`.

### Task 4: Prepare the GPU multi-disk cache contract

**Files:**
- Modify: `backend/domain/job.go`
- Modify: `backend/k8s/rayjob.go`
- Create: `backend/k8s/rayjob_cache_test.go`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/values-prod.yaml.example`
- Modify: `docs/CLUSTER_DEPLOYMENT_GUIDE.md`
- Modify: `docs/USER_GUIDE.md`

- [ ] **Step 1: Write a failing renderer test**

```go
func TestRenderRayJobAddsGenericEphemeralCacheOnlyWhenConfigured(t *testing.T) {
    options := testRenderOptions()
    options.LocalCache = k8s.LocalCacheOptions{Enabled: true, StorageClass: "ray-cache-local", Size: "200Gi"}
    manifest, err := k8s.RenderRayJob(validRenderJob(), options)
    if err != nil { t.Fatal(err) }
    assertGenericEphemeralVolume(t, workerContainer(t, manifest), "local-cache", "ray-cache-local", "/mnt/cache")
    assertEnv(t, workerContainer(t, manifest), "PLATFORM_CACHE_PATH", "/mnt/cache")
}
```

- [ ] **Step 2: Run it and verify RED**

Run: `cd backend && go test ./k8s -run TestRenderRayJobAddsGenericEphemeralCacheOnlyWhenConfigured`

Expected: compile failure because `LocalCacheOptions` is absent.

- [ ] **Step 3: Add explicit cache configuration**

Add `training.localCache.enabled`, `storageClass`, `size` and `mountPath`. Reject partial configuration at startup. When disabled, render no cache volume. When enabled, add a generic ephemeral PVC to each Ray Head/Worker Pod template with `ReadWriteOnce`, the named local StorageClass and `PLATFORM_CACHE_PATH`; never mount it into submitter Pods.

- [ ] **Step 4: Verify renderer behavior**

Run: `cd backend && go test ./k8s -run 'TestRenderRayJob.*Cache'`

Expected: enabled path renders generic ephemeral PVCs; disabled path has neither volume nor cache environment variable.

- [ ] **Step 5: Document node onboarding**

Document the exact operator sequence: mount each data disk, install/configure VKE `csi-local`, create `ray-cache-local`, validate a generic ephemeral PVC on every GPU node, then enable the Helm values. State that `hostPath`, fstab-mounted TOS and personal SSHFS are prohibited for distributed training storage.

### Task 5: Full validation, build and deployment

**Files:**
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: Run all backend tests**

Run: `cd backend && go test ./...`

Expected: exit `0`.

- [ ] **Step 2: Run frontend tests and production build**

Run: `cd frontend && npm test && npm run build`

Expected: exit `0`.

- [ ] **Step 3: Build immutable platform images**

Run on the build host:

```bash
BUILD_TARGETS=backend,frontend PUSH_IMAGE=true IMAGE_TAG=<immutable-tag> bash build-image.sh
```

Expected: both images push successfully to the configured registry.

- [ ] **Step 4: Deploy and verify platform**

Run:

```bash
helm upgrade ray-platform ./helm/ray-train-platform -n ray-train-platform --reuse-values \
  --set backend.image.tag=<immutable-tag> --set frontend.image.tag=<immutable-tag> \
  --wait --timeout 10m
```

Expected: backend and frontend Ready, terminal job logs query through Loki, and a completed storage-backed test job lists its output through the new artifact tab.
