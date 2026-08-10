# Private Ray Submission Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Deliver a private HTTPS submission gateway where Portal users, rayctl, and the stock Ray 2.35 Jobs CLI create the same tenant-scoped Kueue-managed RayJob records.

**Architecture:** Add PAT and immutable source-artifact aggregates to PostgreSQL, authenticate OIDC or PAT at one backend boundary, and translate Ray Jobs requests into the existing TrainingJob workflow. Upload bytes directly to private TOS for Portal/rayctl, keep Ray Head private, and expose only frontend plus backend routes through a private ALB with a cert-manager private CA.

**Tech Stack:** Go 1.24, Gin, GORM/PostgreSQL, Volcengine TOS Go SDK v2 behind an interface, Vue 3/Vite/Vitest, Helm 3, cert-manager, VKE ALB, KubeRay 1.3.0, Kueue 0.19.0, Ray 2.35.0.

---

## File map

- Create backend/db/migrations/0002_submission_gateway.up.sql: token/artifact/job-origin schema.
- Modify backend/db/postgres.go: ordered embedded migration runner.
- Create backend/domain/personal_access_token.go and source_artifact.go: immutable domain types and validation.
- Modify backend/domain/training_job.go: artifact source ID and submission origin.
- Create backend/repositories/personal_access_tokens.go and source_artifacts.go: tenant-safe persistence.
- Create backend/auth/pat.go and hybrid_middleware.go: HMAC PAT verification and OIDC/PAT dispatch.
- Create backend/objectstore/store.go and tos.go: testable presign/head abstraction.
- Create backend/api/personal_access_tokens.go and source_artifacts.go: OIDC-only token management and artifact lifecycle API.
- Create backend/rayapi/types.go, translator.go, handler.go, and associated tests: Ray 2.35 compatibility surface.
- Modify backend/api/jobs.go and backend/main.go: shared submit service and route wiring.
- Modify backend/k8s/rayjob.go: verified artifact materialization.
- Create backend/cmd/rayctl/main.go plus backend/rayctl/archive.go and client.go: cross-platform CLI.
- Add frontend Vitest configuration and tests; create frontend/src/views/AccessTokens/index.vue; modify CreateJob, client, router, and layout.
- Create Helm migration/config/CA/ALB templates and production-private values.
- Create six operator/user runbooks and update the cluster guide.

## Task 1: Ordered migrations and schema

**Files:**
- Test: backend/db/postgres_test.go
- Create: backend/db/migrations/0002_submission_gateway.up.sql
- Modify: backend/db/postgres.go

- [ ] **Step 1: Write the failing migration-order test**

~~~go
func TestMigrationVersionsAreOrdered(t *testing.T) {
    files, err := migrationFiles.ReadDir("migrations")
    if err != nil { t.Fatal(err) }
    got := migrationVersions(files)
    want := []int64{1, 2}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %v want %v", got, want) }
}
~~~

- [ ] **Step 2: Run RED**

Run from project root:

~~~bash
docker run --rm -v /opt/guofeng/vke-cluster/ray-platform/backend:/src -w /src golang:1.24-alpine3.20 go test ./db -run TestMigrationVersionsAreOrdered -count=1
~~~

Expected: FAIL because migrationVersions and migration 2 do not exist.

- [ ] **Step 3: Add the migration and generic runner**

Migration 2 creates personal_access_tokens and source_artifacts, adds source_artifact_id and submission_origin to training_jobs, and adds foreign keys plus owner/expiry and tenant/digest indexes. postgres.go embeds migrations/*.up.sql, parses the four-digit version prefix, sorts ascending, applies each missing version in a transaction, and records it in schema_migrations.

- [ ] **Step 4: Run GREEN and the db package**

Run the same focused command, then go test ./db -count=1. Expected: PASS.

- [ ] **Step 5: Record checkpoint**

This workspace has no .git directory. Record sha256sum for the three changed files in the execution log instead of creating a commit.

## Task 2: PAT cryptography, repository, and API

**Files:**
- Test/Create: backend/domain/personal_access_token_test.go
- Create: backend/domain/personal_access_token.go
- Test/Create: backend/auth/pat_test.go
- Create: backend/auth/pat.go
- Test/Create: backend/repositories/personal_access_tokens_test.go
- Create: backend/repositories/personal_access_tokens.go
- Test/Create: backend/api/personal_access_tokens_test.go
- Create: backend/api/personal_access_tokens.go

- [ ] **Step 1: Write RED tests**

Tests require ParsePAT to reject malformed input, DigestPAT to be deterministic and constant-time comparable, GeneratePAT to return rpt_public_secret with 32 random secret bytes, repository lookup to exclude expired/revoked tokens, OIDC-only creation to return plaintext once, list to omit digest/plaintext, and owner-scoped revoke to return not found across tenants.

~~~go
func TestParsePATRejectsMalformedToken(t *testing.T) {
    for _, value := range []string{"", "rpt_x", "Bearer rpt_x_y", "rpt_bad space_secret"} {
        if _, err := ParsePAT(value); err == nil { t.Fatalf("accepted %q", value) }
    }
}
~~~

- [ ] **Step 2: Run RED**

~~~bash
docker run --rm -v /opt/guofeng/vke-cluster/ray-platform/backend:/src -w /src golang:1.24-alpine3.20 go test ./domain ./auth ./repositories ./api -run 'PAT|PersonalAccessToken' -count=1
~~~

Expected: FAIL because PAT types and handlers do not exist.

- [ ] **Step 3: Implement minimal PAT behavior**

Use crypto/rand for 32 secret bytes; public IDs contain 12 random bytes. Store HMAC-SHA256(pepper, full token), never a fast unsalted hash. Initial scopes are jobs:read, jobs:write, sources:write. Default expiry is 90 days and the request is capped at 365 days. Authentication updates last_used_at at most once per five minutes.

- [ ] **Step 4: Run GREEN and race tests**

Run the focused test, then the full backend suite and go test -race ./auth ./repositories ./api. Expected: PASS.

## Task 3: Hybrid authentication

**Files:**
- Test: backend/auth/middleware_test.go
- Create: backend/auth/hybrid_middleware.go
- Modify: backend/auth/middleware.go
- Modify: backend/main.go
- Modify: backend/config/config.go and config_test.go

- [ ] **Step 1: Write RED matrix**

The matrix covers OIDC token dispatch, rpt_ PAT dispatch, missing authorization in required mode, expired PAT as 401, missing scope as 403 in RequireScopes, and redacted error bodies.

~~~go
func TestHybridAuthenticatorDispatchesPATPrefix(t *testing.T) {
    verifier := &fakePATVerifier{principal: Principal{Subject: "u1", TenantID: "t1"}}
    authn := NewHybridAuthenticator(nil, verifier)
    got, err := authn.Authenticate(context.Background(), "rpt_public_secret")
    if err != nil || got.Subject != "u1" { t.Fatalf("got %+v err %v", got, err) }
}
~~~

- [ ] **Step 2: Run RED**

Run go test ./auth ./config -run 'Hybrid|Pepper|Scope' -count=1. Expected: FAIL for missing hybrid types/config.

- [ ] **Step 3: Implement and wire**

Add PAT_PEPPER through a Secret reference, reject production startup if PAT is enabled without at least 32 bytes of pepper, use OIDC-only middleware for token-management routes, and hybrid middleware for jobs/artifacts/Ray routes. Demo mode may create a PAT for the fixed local demo principal, but production never permits anonymous token creation.

- [ ] **Step 4: Run GREEN**

Run auth/config tests and full backend tests. Expected: PASS without printing tokens.

## Task 4: Immutable source artifacts and TOS

**Files:**
- Create/Test: backend/domain/source_artifact.go and source_artifact_test.go
- Create/Test: backend/repositories/source_artifacts.go and source_artifacts_test.go
- Create/Test: backend/objectstore/store.go, tos.go, and tos_test.go
- Create/Test: backend/api/source_artifacts.go and source_artifacts_test.go
- Modify/Test: backend/domain/training_job.go and training_job_test.go

- [ ] **Step 1: Write RED tests**

Validate lowercase 64-character SHA256, size 1 through 2 GiB, deterministic tenant/user object key, 15-minute expiry, idempotent tenant/digest creation, cross-tenant 404, and complete only after HEAD size plus x-tos-meta-sha256 match.

~~~go
func TestArtifactObjectKeyIsTenantScoped(t *testing.T) {
    got, err := ObjectKey("tenant-a", "user-a", strings.Repeat("a", 64))
    if err != nil { t.Fatal(err) }
    want := "tenants/tenant-a/users/user-a/sha256/" + strings.Repeat("a", 64) + ".zip"
    if got != want { t.Fatalf("got %s", got) }
}
~~~

- [ ] **Step 2: Run RED**

Run go test ./domain ./repositories ./objectstore ./api -run Artifact -count=1. Expected: FAIL.

- [ ] **Step 3: Implement the store boundary**

~~~go
type Store interface {
    PresignPut(context.Context, string, string, int64, string, time.Duration) (PresignedUpload, error)
    Head(context.Context, string, string) (ObjectInfo, error)
}
~~~

Use the official ve-tos-golang-sdk/v2 client only in tos.go. The API returns uploadUrl, requiredHeaders, expiresAt, artifactId, and object size/digest metadata; it never returns AK/SK. Complete performs HEAD and marks state READY atomically.

- [ ] **Step 4: Run GREEN**

Run focused, full, and race tests. Expected: PASS.

## Task 5: Artifact materialization and unified submission

**Files:**
- Test/Modify: backend/domain/training_job_test.go and training_job.go
- Test/Modify: backend/k8s/rayjob_test.go and rayjob.go
- Test/Modify: backend/api/jobs_test.go and jobs.go
- Create/Test: backend/api/submission_service.go and submission_service_test.go

- [ ] **Step 1: Write RED tests**

Artifact jobs require artifactId, the service verifies READY plus tenant ownership before persistence, submissionOrigin is portal/api/ray-cli, and RayJob initContainer downloads exactly the immutable artifact key and verifies SHA256 before safe extraction.

- [ ] **Step 2: Run RED**

Run go test ./domain ./k8s ./api -run 'Artifact|SubmissionOrigin' -count=1. Expected: FAIL.

- [ ] **Step 3: Implement minimal shared service**

Move queue enforcement, allowlist checks, identity persistence, quota handling, ID generation, and repository Create into SubmissionService.Submit. Browser and Ray handlers both call this service. The materializer uses a dedicated safe-extract binary from the source-materializer image rather than shell unzip.

- [ ] **Step 4: Run GREEN**

Run focused and full backend suites. Expected: PASS.

## Task 6: Ray 2.35 Jobs compatibility

**Files:**
- Create/Test: backend/rayapi/types.go, translator.go, translator_test.go, handler.go, handler_test.go
- Modify: backend/main.go

- [ ] **Step 1: Write RED contract tests**

Cover GET version, gcs package HEAD/PUT, job submit/list/get/stop/delete/logs, invalid protocol, invalid digest package name, missing metadata, scope enforcement, tenant isolation, JSON field names expected by Ray 2.35, and streaming size rejection.

~~~go
func TestPackageNameRejectsPathTraversal(t *testing.T) {
    for _, value := range []string{"../x.zip", "x/y.zip", "working_dir.zip"} {
        if _, err := ParsePackageName("gcs", value); err == nil { t.Fatalf("accepted %s", value) }
    }
}
~~~

- [ ] **Step 2: Run RED**

Run go test ./rayapi -count=1. Expected: FAIL because package is absent.

- [ ] **Step 3: Implement translator and handlers**

Address https://ray-platform.internal/ray maps Ray client requests to /ray/api. Accept only gcs and digest-named ZIP packages. Translate metadata keys ray-platform.image, worker-replicas, gpus-per-worker, cpu-per-worker, memory-per-worker, and queue through strict schema validation. Persist before RayJob reconciliation and return Ray-compatible submission_id/status/message fields.

- [ ] **Step 4: Run GREEN and real-client contract**

Run package tests, full backend tests, then start the backend test server and execute Ray 2.35 JobSubmissionClient against it from the pinned training image. Expected: version, submit, list, get, and logs deserialize successfully.

## Task 7: rayctl local packaging and submission

**Files:**
- Create/Test: backend/rayctl/archive.go, archive_test.go, client.go, client_test.go
- Create: backend/cmd/rayctl/main.go
- Modify: backend/Dockerfile or create backend/Dockerfile.rayctl for release binaries

- [ ] **Step 1: Write RED archive tests**

Tests cover deterministic ZIP order/timestamps, .gitignore plus .rayignore, no .git inclusion, escaping symlink rejection, 2 GiB logical limit, SHA256, direct PUT with required headers, complete, then submit.

- [ ] **Step 2: Run RED**

Run go test ./rayctl -count=1. Expected: FAIL.

- [ ] **Step 3: Implement minimal CLI**

Commands are login-check, package, submit, status, logs, and cancel. Token comes from RAY_PLATFORM_TOKEN or a mode-0600 config file; CA comes from --ca-file or SSL_CERT_FILE. The CLI never prints the token or upload URL unless --debug is explicitly set, and debug still redacts signatures.

- [ ] **Step 4: Run GREEN and cross-build**

Run tests, then CGO_ENABLED=0 go build for linux/amd64, darwin/arm64, and windows/amd64 in the Go container. Expected: three binaries and checksums.

## Task 8: Portal PAT and local ZIP flows

**Files:**
- Modify: frontend/package.json and package-lock.json
- Create: frontend/vitest.config.js
- Create/Test: frontend/src/api/artifacts.js and artifacts.test.js
- Create/Test: frontend/src/views/AccessTokens/index.vue and index.test.js
- Modify/Test: frontend/src/views/Job/CreateJob.vue and CreateJob.test.js
- Modify: frontend/src/api/client.js, frontend/src/router/index.js, frontend/src/layout/Layout.vue

- [ ] **Step 1: Add Vitest and write RED component/API tests**

Tests assert copy-once token display then clearing, metadata-only list, revoke confirmation, ZIP type/size validation, upload progress, complete-before-submit ordering, artifact source payload, and private GitLab-neutral wording.

- [ ] **Step 2: Run RED**

Run npm test -- --run. Expected: FAIL because views/helpers do not exist.

- [ ] **Step 3: Implement minimal UI**

Use Web Crypto for SHA256, XMLHttpRequest for direct PUT progress, never persist PAT plaintext in localStorage/sessionStorage, and clear it on dialog close. Add Local package radio choice and artifact details.

- [ ] **Step 4: Run GREEN and production build**

Run npm test -- --run and npm run build. Expected: PASS and dist generated without warnings treated as errors.

## Task 9: Private CA, ALB, and Helm

**Files:**
- Create: helm/ray-train-platform/templates/private-ca.yaml
- Create: helm/ray-train-platform/templates/alb-instance.yaml
- Modify: helm/ray-train-platform/templates/vke-alb-ingress.yaml
- Modify: helm/ray-train-platform/templates/backend-deployment.yaml
- Modify: helm/ray-train-platform/values.yaml and values-test.yaml.example
- Create: helm/ray-train-platform/values-private.yaml.example

- [ ] **Step 1: Write RED render checks**

Create scripts/test-helm-private.sh to assert TLS secret ray-platform-tls, private ALBInstance, HTTPS 443, backend-first paths /api/v1, /ray/api, /workspace, frontend catch-all, PAT Secret refs, 2 GiB compatibility-body limit, and no plaintext credentials.

- [ ] **Step 2: Run RED**

Run bash scripts/test-helm-private.sh. Expected: FAIL because CA/ALB templates and routes are absent.

- [ ] **Step 3: Implement templates**

Use cert-manager self-signed bootstrap, CA certificate, CA ClusterIssuer/Issuer, and server Certificate for ray-platform.internal. Gate ALBInstance creation behind explicit private subnet IDs and allowed CIDRs. Prefer TLS Secret binding; provide a separate scoped certificate-center synchronization script only when controller preflight proves native Secret binding unsupported.

- [ ] **Step 4: Run GREEN**

Run helm lint, helm template for test and private examples, kubectl apply --dry-run=server for rendered resources, and scripts/test-helm-private.sh. Expected: PASS.

## Task 10: Documentation, build, deploy, and E2E

**Files:**
- Create: docs/PRIVATE_ALB_DEPLOYMENT.md
- Create: docs/CERT_MANAGER_PRIVATE_CA.md
- Create: docs/USER_SUBMISSION_GUIDE.md
- Create: docs/RAY_CLI_COMPATIBILITY.md
- Create: docs/TOS_SOURCE_ARTIFACTS.md
- Create: docs/TRAINING_E2E_RUNBOOK.md
- Modify: docs/CLUSTER_DEPLOYMENT_GUIDE.md
- Modify: scripts/e2e-training.sh

- [ ] **Step 1: Write documentation validation test**

scripts/test-docs.sh checks every runbook for prerequisites, exact validation commands, failure symptoms, rollback, and billable-resource notes; examples must use ray-platform.internal and never contain credential literals.

- [ ] **Step 2: Run RED, write runbooks, run GREEN**

Run bash scripts/test-docs.sh before and after writing. Expected: initial FAIL, final PASS.

- [ ] **Step 3: Full verification**

Run backend full/race tests, frontend tests/build, Helm lint/template/dry-run, shellcheck when available, docker image smoke tests, and a secret-pattern scan. Build immutable backend/frontend/source-materializer images, push to the logged-in registry, capture digests, and upgrade Helm with --atomic --wait.

- [ ] **Step 4: Private network acceptance**

Install cert-manager, issue CA/server cert, provision private ALB, bind temporary hosts mapping, verify browser TLS, create PAT, submit one-GPU smoke job from Portal/rayctl, submit again with stock Ray 2.35 CLI, verify both appear in Portal, inspect Kueue admission and CUDA probe logs, revoke PAT, and confirm 401.

- [ ] **Step 5: Rollback evidence**

Record Helm revision, ALB ID/VIP, certificate serial, image digests, job IDs, test outputs, and exact rollback commands. Do not delete source artifacts or cloud resources automatically; the runbook states explicit, scoped cleanup actions.

## Plan self-review

- Spec coverage: PAT, direct TOS, Ray CLI, Portal, unified DB, Kueue/RayJob, private ALB/TLS, security, docs, and E2E all map to Tasks 1-10.
- Type consistency: SourceArtifact.ArtifactID flows through CodeSource.ArtifactID, SubmissionService, repository foreign key, and materializer. Principal and scopes are shared by Portal API and Ray API.
- Security boundary: Ray Head remains ClusterIP; only backend creates TrainingJob/RayJob; every object key is derived server-side.
- Execution choice: the user already requested implementation, so execution proceeds inline with bounded specialist review at checkpoints. No extra handoff question is needed.
- Repository limitation: no .git exists; checksum checkpoints and deployment revision evidence replace commits without inventing repository history.
