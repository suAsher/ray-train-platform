# External rayctl Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a secure, self-service external code submission route for Ray training users.

**Architecture:** Keep the current Portal ingress unchanged and add an HTTPS ingress for `raytrain.wellspiking.ai`. Build `rayctl` as native static binaries and distribute them through a two-replica in-cluster release service. The CLI sends a PAT only to the HTTPS platform API, uploads source directly to the already-governed TOS artifact API, and relies on server-side tenant queue resolution.

**Tech Stack:** Go, Gin, Kubernetes Helm, VKE ALB Ingress, nginx, TOS pre-signed upload, existing PAT and source-artifact APIs.

---

### Task 1: Secure CLI connection configuration

**Files:**
- Create: `backend/rayctl/config_test.go`
- Modify: `backend/rayctl/client.go`
- Modify: `backend/rayctl/command.go`
- Modify: `backend/rayctl/command_test.go`

- [ ] **Step 1: Write failing tests for token-stdin login and secure config mode**

```go
func TestRunLoginWritesOwnerOnlyConfigFromStandardInput(t *testing.T) {
    config := filepath.Join(t.TempDir(), "config.json")
    err := RunWithInput(context.Background(), []string{
        "login", "--server", "https://raytrain.wellspiking.ai", "--token-stdin", "--config", config,
    }, strings.NewReader("rpt_example\\n"), &stdout, &stderr, nil)
    require.NoError(t, err)
    require.Equal(t, os.FileMode(0600), mode(config))
}

func TestRunLoginRejectsHTTPServer(t *testing.T) {
    err := RunWithInput(context.Background(), []string{
        "login", "--server", "http://train.xx.com", "--token-stdin",
    }, strings.NewReader("rpt_example\\n"), &stdout, &stderr, nil)
    require.Error(t, err)
}
```

- [ ] **Step 2: Run the focused Go test and verify the missing login command fails**

Run: `cd backend && go test ./rayctl -run 'TestRunLogin' -count=1`

Expected: failure because `login` and `RunWithInput` are not implemented.

- [ ] **Step 3: Implement owner-only config write and login command**

Implement `RunWithInput` as the testable command entry point; retain `Run` as a
wrapper using `os.Stdin`. Validate the server with `NewClient`, trim a single
line token, atomically write JSON through a mode-0700 parent directory and a
mode-0600 temporary file, then rename it. Do not accept a `--token` flag and do
not print the token.

- [ ] **Step 4: Add a version command and make tenant queue optional**

Add `rayctl version`, injected by Go linker flags. Remove `--queue` from the
required `submit` flags; leave it absent in `domain.JobSpec` so
`normalizeSubmissionSpec` derives the authenticated tenant queue.

- [ ] **Step 5: Run focused tests**

Run: `cd backend && go test ./rayctl -count=1`

Expected: pass.

### Task 2: Release image and Helm distribution service

**Files:**
- Modify: `backend/Dockerfile.rayctl`
- Modify: `build-image.sh`
- Create: `helm/ray-train-platform/templates/rayctl-release.yaml`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Test: `scripts/test-delivery-render.sh`

- [ ] **Step 1: Write a render assertion for the release deployment and HTTPS route**

Add assertions that the CPU HA render contains a two-replica `rayctl-release`
Deployment, its Service, a TLS Secret reference named `raytrain-web-tls`, and a
`/downloads/rayctl/` backend route.

- [ ] **Step 2: Run the render script and verify it fails before templates exist**

Run: `bash scripts/test-delivery-render.sh`

Expected: failure because the release service and HTTPS ingress route are absent.

- [ ] **Step 3: Build static binaries into an nginx release image**

Use `backend/Dockerfile.rayctl` to compile `linux-amd64`, `darwin-arm64`, and
`windows-amd64.exe`, generate `SHA256SUMS`, and copy them beneath
`/usr/share/nginx/html/downloads/rayctl/` in a non-root nginx image. Add the
`rayctl` target to `build-image.sh` with the same immutable tag flow as all
other images.

- [ ] **Step 4: Render an HA download service and separate HTTPS ingress**

Add a two-replica, CPU-node-selected `rayctl-release` Deployment and ClusterIP
Service. Create a separate ALB Ingress for `raytrain.wellspiking.ai` using
port 443, protocol HTTPS, standard `spec.tls`, `raytrain-web-tls`, and routes
where `/downloads/rayctl/` targets the release service while `/` targets the
frontend.

- [ ] **Step 5: Run render tests and Helm lint**

Run: `bash scripts/test-delivery-render.sh && helm lint ./helm/ray-train-platform --values deploy/profiles/vke-cpu-ha.yaml`

Expected: both commands pass.

### Task 3: Portal guidance and artifact production activation

**Files:**
- Create: `frontend/src/views/ExternalSubmit/index.vue`
- Modify: `frontend/src/router/index.js`
- Modify: `frontend/src/layout/Layout.vue`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Test: `frontend/src/productionWorkflow.test.js`

- [ ] **Step 1: Add a failing source-level UI test**

Assert that the route and navigation include “外部提交”, that the page links to
the HTTPS download path, and that it documents `rayctl login` and
`rayctl submit` without exposing a bucket endpoint or credential.

- [ ] **Step 2: Run the UI test and verify it fails**

Run: `cd frontend && npm test -- --run src/productionWorkflow.test.js`

Expected: failure because the external submission route and text do not exist.

- [ ] **Step 3: Implement the concise guided page**

Show prerequisite, download, PAT creation, login, submit, status/log/cancel
commands and explicit warning that `pip install ray` is for local debugging only.
Enable `backend.sourceArtifactsEnabled` in the CPU HA profile.

- [ ] **Step 4: Run frontend unit tests and production build**

Run: `cd frontend && npm test -- --run src/productionWorkflow.test.js && npm run build`

Expected: tests and production bundle pass.

### Task 4: Build, deploy, and external acceptance

**Files:**
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Test: `scripts/e2e_external_rayctl.sh`

- [ ] **Step 1: Add an acceptance script that never prints credentials**

The script creates a temporary local user through an administrator session,
issues the minimum job/source PAT, downloads the release binary through HTTPS,
checks its SHA256, writes a temporary source directory, submits one GPU, waits
for `SUCCEEDED`, obtains logs, revokes the PAT, verifies a subsequent status
call is rejected, then removes temporary local resources.

- [ ] **Step 2: Build immutable backend, frontend, and rayctl images on the build host**

Run: `BUILD_TARGETS=backend,frontend,rayctl PUSH_IMAGE=true IMAGE_TAG=prod-<timestamp> bash build-image.sh`

Expected: pushed image digests are recorded without credentials.

- [ ] **Step 3: Create the TLS Secret without committing the certificate**

Run on the build host: `kubectl -n ray-train-platform create secret tls raytrain-web-tls --cert <fullchain> --key <private-key> --dry-run=client -o yaml | kubectl apply -f -`

Expected: Secret is present; PEM input is not printed.

- [ ] **Step 4: Deploy atomically and verify both ingress paths**

Run: `bash ops/platform/deploy.sh --profile deploy/profiles/vke-cpu-ha.yaml --verify-fsx-irsa --timeout 15m`

Then verify all release replicas and use `curl --resolve` against ALB VIP to
check the certificate, UI, API, and release checksum path.

- [ ] **Step 5: Run the full external acceptance script and document DNS handoff**

Run: `bash scripts/e2e_external_rayctl.sh --api-url https://raytrain.wellspiking.ai --resolve 172.28.6.171`

Expected: one external RayJob reaches `SUCCEEDED`; no token or pre-signed URL
appears in the output. Record that internal DNS must map the new host to the
private ALB VIP before users install it.
