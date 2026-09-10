# Phase A Access Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore debug editor access and make artifact/MLflow navigation correct in both the standalone RayTrain UI and the migrated Portal without changing running Ray workloads.

**Architecture:** Keep authenticated API issuance inside the normal OAuth/PAT/session middleware, but mount browser-navigation proxies outside that middleware so their own resource-scoped signed tokens can be exchanged. Keep backend URLs root-relative for standalone compatibility and adapt them at the Portal browser boundary to the `/raytrain` prefix. Use the existing global job lookup only to distinguish invisible jobs from visible-but-private artifacts.

**Tech Stack:** Go 1.22, Gin, Vue 3, Element Plus, Node contract tests, Kubernetes NGINX Ingress, Helm.

---

### Task 1: Mount the workspace browser proxy outside generic authentication

**Files:**
- Create: `backend/main_workspace_proxy_routes_test.go`
- Modify: `backend/main.go:295-335`

- [ ] **Step 1: Write the failing route-order integration test**

```go
func TestWorkspaceProxyTokenReachesResourceScopedVerifierBeforeOAuth(t *testing.T) {
    router := gin.New()
    jobs := api.NewHandler(&mainJobRepository{}, api.Options{
        WorkspacePepper: []byte(strings.Repeat("p", 32)),
    })
    registerAPIRoutesWithLocalAuth(router, jobs, nil, nil, nil, nil, nil, nil, nil, nil, nil, config.Config{
        OAuth2ProxyAuthEnabled: true,
    })
    request := httptest.NewRequest(http.MethodGet,
        "/api/v1/dev-workspaces/ws-1/proxy/?access_token=invalid&subject=user-1", nil)
    response := httptest.NewRecorder()
    router.ServeHTTP(response, request)
    if !strings.Contains(response.Body.String(), "WORKSPACE_ACCESS_INVALID") {
        t.Fatalf("workspace token verifier was bypassed: %d %s", response.Code, response.Body.String())
    }
}
```

- [ ] **Step 2: Run the focused test on the build host and verify RED**

Run: `go test ./... -run TestWorkspaceProxyTokenReachesResourceScopedVerifierBeforeOAuth -count=1`

Expected: FAIL because the generic OAuth middleware returns `AUTH_REQUIRED` before the workspace handler.

- [ ] **Step 3: Move only the proxy registration**

Register the self-authorizing browser route before `protected := router.Group("")`:

```go
jobs.RegisterWorkspaceProxyRoute(router.Group("/api/v1"))
```

Keep `jobs.RegisterWorkspaceRoutes(v1)` inside the interactive authenticated group; do not expose workspace create, stop, list, or access-ticket issuance.

- [ ] **Step 4: Run route and workspace tests and verify GREEN**

Run: `go test ./... -run 'TestWorkspaceProxy|TestWorkspaceAccess' -count=1`

Expected: PASS, including invalid, expired, wrong-workspace and cookie-scoping checks.

- [ ] **Step 5: Commit the backend route fix**

```bash
git add backend/main.go backend/main_workspace_proxy_routes_test.go
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit -m "fix: allow scoped workspace proxy access"
```

### Task 2: Make artifact visibility and authorization semantically consistent

**Files:**
- Modify: `backend/api/job_artifacts_test.go`
- Modify: `backend/api/job_artifacts.go:188-196`

- [ ] **Step 1: Write failing SuperAdmin visibility tests**

Add one test for an invisible foreign-tenant Engineer expecting 404 and one for a SuperAdmin viewing a foreign personal-output task expecting 403 without touching object storage:

```go
func TestJobArtifactRouteReturnsForbiddenForVisibleForeignPersonalOutput(t *testing.T) {
    job := artifactJob("job-b", "tenant-b")
    job.UserID = "user-b"
    job.Spec.ResolvedStorage = domain.ResolvedStorageMounts{}
    job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
        Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
        ClaimName: "data-user-b", SubPath: "runs/job-b",
        MountPath: domain.DataMountOutputPath,
    }
    lister := &fakeArtifactLister{}
    handler := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{ArtifactLister: lister})
    router := artifactRouter(handler, auth.Principal{
        Subject: "admin", TenantID: "local", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal,
    })
    response := httptest.NewRecorder()
    router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-b/artifacts", nil))
    if response.Code != http.StatusForbidden || lister.taskRoot != "" {
        t.Fatalf("status=%d root=%q body=%s", response.Code, lister.taskRoot, response.Body.String())
    }
}
```

- [ ] **Step 2: Run the focused test on the build host and verify RED**

Run: `go test ./api -run 'TestJobArtifactRoute(ReturnsForbidden|RejectsOtherTenant)' -count=1`

Expected: the new SuperAdmin test receives 404 because `authorizedArtifactJob` performs tenant-only lookup.

- [ ] **Step 3: Reuse the existing global-aware lookup**

```go
func (h *Handler) authorizedArtifactJob(c *gin.Context, principal auth.Principal) (*domain.TrainingJob, bool) {
    job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
    if err != nil {
        h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
        return nil, false
    }
    return job, true
}
```

The existing `jobArtifactRoot` owner checks must remain unchanged so global task visibility never grants checkpoint access.

- [ ] **Step 4: Run artifact and API tests and verify GREEN**

Run: `go test ./api -run 'TestJobArtifact' -count=1`

Expected: PASS; foreign Engineer remains 404, SuperAdmin receives 403 for personal artifacts, and the lister/reader is not invoked.

- [ ] **Step 5: Commit the artifact fix**

```bash
git add backend/api/job_artifacts.go backend/api/job_artifacts_test.go
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit -m "fix: distinguish visible private job artifacts"
```

### Task 3: Add a safe Portal browser-navigation adapter

**Files (Portal repository):**
- Create: `src/views/rayTrain/api/browserNavigation.js`
- Create: `scripts/check-raytrain-access-contract.mjs`
- Modify: `src/views/rayTrain/api/mlflowDashboard.js`
- Modify: `src/views/rayTrain/Devcenter/index.vue:335-352`
- Modify: `docker/Dockerfile.lint`

- [ ] **Step 1: Write the failing Node contract test**

The script imports the new helper and asserts exact accepted and rejected inputs:

```js
assert.equal(rayTrainBrowserURL('/mlflow/?access_token=abc_123'), '/raytrain/mlflow/?access_token=abc_123')
assert.equal(
  rayTrainBrowserURL('/api/v1/dev-workspaces/ws-1/vscode/?access_token=abc_123&subject=user-1'),
  '/raytrain/api/v1/dev-workspaces/ws-1/vscode/?access_token=abc_123&subject=user-1'
)
for (const unsafe of ['https://evil.example/mlflow/', '//evil.example/x', 'javascript:alert(1)', '/api/v1/jobs']) {
  assert.throws(() => rayTrainBrowserURL(unsafe))
}
```

Add `RUN node scripts/check-raytrain-access-contract.mjs` to `docker/Dockerfile.lint`.

- [ ] **Step 2: Build the Portal lint image on the build host and verify RED**

Run: `docker build --pull -f docker/Dockerfile.lint .`

Expected: FAIL because `browserNavigation.js` does not yet exist or does not satisfy the contract.

- [ ] **Step 3: Implement the allowlisted adapter**

```js
const allowedPath = /^\/(?:mlflow\/|api\/v1\/dev-workspaces\/[A-Za-z0-9-]+\/(?:proxy|vscode)\/)/

export function rayTrainBrowserURL(value) {
  if (typeof value !== 'string' || /[\u0000-\u0020\u007f\\]/.test(value) || value.includes('#')) {
    throw new Error('平台返回的访问地址不安全')
  }
  const base = new URL('https://portal.invalid')
  const parsed = new URL(value, base)
  if (parsed.origin !== base.origin || !allowedPath.test(parsed.pathname)) {
    throw new Error('平台返回的访问地址不安全')
  }
  return `/raytrain${parsed.pathname}${parsed.search}`
}
```

Retain the stricter MLflow access-token validation, then pass the validated relative URL through `rayTrainBrowserURL`. In Devcenter, stop appending a duplicate `subject`; open only the adapted signed URL returned by the backend.

- [ ] **Step 4: Build the Portal lint image and verify GREEN**

Run: `docker build --pull -f docker/Dockerfile.lint .`

Expected: `lint:check`, `check:ep`, `check:store`, route contract and access contract all pass.

- [ ] **Step 5: Commit the navigation adapter**

```bash
git add src/views/rayTrain/api/browserNavigation.js src/views/rayTrain/api/mlflowDashboard.js src/views/rayTrain/Devcenter/index.vue scripts/check-raytrain-access-contract.mjs docker/Dockerfile.lint
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit -m "fix: adapt RayTrain browser links for Portal"
```

### Task 4: Add job-scoped MLflow navigation and avoid unauthorized artifact requests

**Files (Portal repository):**
- Create: `src/views/rayTrain/jobArtifactAccess.js`
- Modify: `src/views/rayTrain/Job/JobDetail.vue`
- Modify: `scripts/check-raytrain-access-contract.mjs`

- [ ] **Step 1: Extend the failing contract test**

```js
assert.equal(canBrowseJobArtifacts({ userId: 'u1' }, 'u1'), true)
assert.equal(canBrowseJobArtifacts({ userId: 'u2' }, 'u1'), false)
assert.match(jobDetailSource, /requestMLflowDashboardAccess\(\{ runId: experiment\.value\.run\.id \}\)/)
assert.match(jobDetailSource, /v-if="canBrowseArtifacts"/)
```

- [ ] **Step 2: Run the Portal lint build and verify RED**

Run: `docker build --pull -f docker/Dockerfile.lint .`

Expected: FAIL because job-scoped MLflow navigation and artifact gating are absent.

- [ ] **Step 3: Implement owner-safe artifact UI and MLflow button**

```js
export function canBrowseJobArtifacts(job, currentUserID) {
  return Boolean(job?.userId && currentUserID && job.userId === currentUserID)
}
```

Add a computed `canBrowseArtifacts`, render `JobArtifactBrowser` only for the owner, and show an informational alert otherwise. Add an “打开 MLflow” button when `experiment?.run?.id` exists. Pre-open a blank tab synchronously, request a run-scoped URL, use the safe navigation adapter, and close the tab on failure.

- [ ] **Step 4: Build the Portal lint image and verify GREEN**

Run: `docker build --pull -f docker/Dockerfile.lint .`

Expected: all lint and RayTrain contract gates pass.

- [ ] **Step 5: Commit the task-detail fix**

```bash
git add src/views/rayTrain/jobArtifactAccess.js src/views/rayTrain/Job/JobDetail.vue scripts/check-raytrain-access-contract.mjs
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit -m "feat: open task MLflow runs from Portal"
```

### Task 5: Proxy MLflow through only the approved Portal prefix

**Files:**
- Modify: `deploy/portal/test-dev-raytrain-ingress.yaml`
- Modify: `deploy/portal/common-raytrain-ingress.yaml`
- Create: `ops/platform/test/portal-raytrain-ingress-test.sh`
- Modify: `scripts/test-delivery-render.sh`

- [ ] **Step 1: Write the failing manifest contract**

```bash
for manifest in "$root/deploy/portal/test-dev-raytrain-ingress.yaml" "$root/deploy/portal/common-raytrain-ingress.yaml"; do
  grep -Fq '/raytrain/(api/.*|ray/.*|mlflow/.*)' "$manifest"
  ! grep -Fq '/raytrain/(.*)' "$manifest"
  grep -Fq 'nginx.ingress.kubernetes.io/rewrite-target: /$1' "$manifest"
done
```

Invoke this script from `scripts/test-delivery-render.sh`.

- [ ] **Step 2: Run the contract on the build host and verify RED**

Run: `bash ops/platform/test/portal-raytrain-ingress-test.sh`

Expected: FAIL because both manifests omit `mlflow/.*`.

- [ ] **Step 3: Add only the MLflow prefix**

Change both paths to:

```yaml
- path: /raytrain/(api/.*|ray/.*|mlflow/.*)
```

Do not change host, service, auth annotations, rewrite target, or backend TLS settings.

- [ ] **Step 4: Run delivery contracts and verify GREEN**

Run: `bash ops/platform/test/portal-raytrain-ingress-test.sh`

Expected: PASS for both exact allowlists and rejection of the catch-all pattern.

- [ ] **Step 5: Commit the Ingress contract**

```bash
git add deploy/portal/test-dev-raytrain-ingress.yaml deploy/portal/common-raytrain-ingress.yaml ops/platform/test/portal-raytrain-ingress-test.sh scripts/test-delivery-render.sh
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit -m "feat: proxy MLflow through Portal gateway"
```

### Task 6: Full verification, synchronization and controlled rollout

**Files:**
- Modify only release overlays generated outside Git; do not edit running Ray resources.

- [ ] **Step 1: Run the full backend suite on a detached build-host worktree**

Run: `go test -timeout=20m ./...`

Expected: all packages PASS with no skipped new tests.

- [ ] **Step 2: Run Portal CI-equivalent validation from a clean archive on the build host**

Run: `docker build --pull -f docker/Dockerfile.lint .`

Expected: lint, Element Plus, store, route and access contracts all pass.

- [ ] **Step 3: Perform the security diff review**

Verify no secrets, raw OAuth identity trust, open proxy path, token logging, arbitrary external URL, cross-tenant artifact read or catch-all Ingress route was introduced.

- [ ] **Step 4: Synchronize reviewed commits**

Fast-forward backend `main`, push GitHub and internal GitLab, transfer a git bundle to the build host, and verify all four backend SHAs match. Push Portal commits to internal GitLab `dev` as `guofeng.su`; CI performs its own lint and deployment.

- [ ] **Step 5: Build and deploy only the backend image**

Build with `BUILD_TARGETS=backend`; inspect its registry digest. Save Helm values/manifests and RayJob/RayCluster baselines, perform server-side dry-run, and apply an atomic backend-only upgrade. Keep OAuth2 Proxy and local session compatibility enabled.

- [ ] **Step 6: Apply only the dev Portal Ingress after a server-side diff**

Use `~/.kube/test-dev.conf` in namespace `guofeng-su`. Do not apply the common/formal Ingress until the Portal dev acceptance checks pass.

- [ ] **Step 7: Run production acceptance checks**

Expected:

- standalone `/` and `/login` return 200 with no redirect;
- Portal SPA returns 200 and anonymous API returns 401;
- workspace signed URL exchanges to a scoped cookie instead of generic OAuth 401;
- cross-tenant SuperAdmin artifact request returns 403 rather than 404 and does not read object storage;
- a task with an MLflow Run opens through `/raytrain/mlflow/`;
- `spk-rayjob` PAT endpoints remain at `raytrain.wellspiking.ai`;
- RayJob and RayCluster baseline diffs remain empty.
