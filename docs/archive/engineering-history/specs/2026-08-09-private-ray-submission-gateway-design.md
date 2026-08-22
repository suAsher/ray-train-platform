# Private Ray Submission Gateway Design

**Date:** 2026-08-09
**Status:** Approved direction, pending written-spec review
**Target:** VKE cluster `81533555-kd9q3q0frjmp21sk5lklg`, Ray 2.35.0, KubeRay 1.3.0

## 1. Goal

Provide one private HTTPS entry point at `https://ray-platform.internal` so users can:

1. submit training jobs from the browser;
2. submit a local working directory from their own workstation without SSH access;
3. use the stock Ray 2.35.0 Jobs CLI through a compatible gateway;
4. see jobs, state, logs, metrics, and artifacts in the browser regardless of submission method.

All job creation must still pass through platform authentication, tenant isolation, image policy, Kueue admission, audit logging, and the platform database. Ray Head services and the Kubernetes API remain private ClusterIP/control-plane endpoints.

## 2. Confirmed Constraints

- The entry point is private, reachable only from the VPC, VPN, or private line.
- No public DNS name or public ALB is required.
- The temporary internal hostname is `ray-platform.internal`.
- TLS uses a cert-manager-managed private CA. Client machines install the CA certificate.
- Users authenticate CLI automation with personal access tokens (PATs).
- Local source is uploaded to a private TOS bucket owned by the platform.
- Default source-package limit is 2 GiB.
- Unreferenced uploads expire after 7 days; referenced job source is retained for 30 days.
- Private GitLab server-side cloning and user-managed Git credentials are not part of V1. Git credentials remain on the user's workstation when the direct-upload flow is used.
- Deployment, access, certificate trust, DNS, upgrade, rollback, and smoke-test procedures are project documentation deliverables.

## 3. Architecture

```text
User workstation
  |-- Browser
  |-- rayctl (preferred local-source path)
  `-- ray job submit (Ray 2.35 compatibility path)
            |
            | HTTPS + OIDC/PAT
            v
Private VKE ALB :443  <--- ray-platform.internal
  |-- /                         -> frontend Service
  |-- /api/v1/*                 -> backend Service
  |-- /ray/api/*                -> backend Ray-compatible gateway
  `-- /workspace/*              -> authenticated workspace proxy
                                      |
                  +-------------------+-------------------+
                  |                   |                   |
              PostgreSQL         Private TOS       Kubernetes API
            users/PAT/jobs       source bundles      RayJob CRs
                                                      |
                                                   Kueue
                                                      |
                                               Ray head/workers
```

The ALB is the only user-facing network entry point. `/api/v1` and `/ray/api` route directly to the backend, while `/` routes to the frontend. Jupyter and Ray Dashboard access remains behind the existing authenticated backend proxy.

## 4. Authentication and Personal Access Tokens

### 4.1 Browser

The browser continues to use Keycloak OIDC. PAT management endpoints accept OIDC authentication only; a PAT cannot create another PAT.

### 4.2 PAT format and storage

- Token format: `rpt_<public-id>_<32-byte-random-secret>`.
- Plaintext is returned exactly once at creation.
- PostgreSQL stores the public ID, an HMAC-SHA256 digest produced with a server-side pepper, token scopes, owner, tenant, expiry, last-used timestamp, and revocation timestamp.
- The pepper is loaded from a Kubernetes Secret and never stored in PostgreSQL or logs.
- Default expiry is 90 days; maximum expiry is 365 days.
- Initial scopes are jobs:read, jobs:write, and sources:write. PAT management remains OIDC-only.
- Revoked or expired tokens return HTTP 401. A valid token lacking a scope returns HTTP 403.
- Authentication failures are rate-limited by source IP and token public ID.

### 4.3 PAT API

- `POST /api/v1/personal-access-tokens` creates a token and returns plaintext once.
- `GET /api/v1/personal-access-tokens` lists metadata only.
- `DELETE /api/v1/personal-access-tokens/{id}` revokes a token owned by the current user.

## 5. Source Artifact Upload

### 5.1 Preferred `rayctl` flow

rayctl is a small, cross-platform Go CLI shipped by this project so users can install a single binary without a Python runtime dependency.

1. `rayctl submit` walks the selected working directory, applies `.gitignore` plus `.rayignore`, rejects symlinks escaping the root, creates a deterministic ZIP, and computes SHA256 and size.
2. `POST /api/v1/source-artifacts` validates the declared digest and size and returns a 15-minute pre-signed TOS PUT URL plus required headers.
3. The workstation uploads directly to TOS. Package bytes do not traverse the backend or ALB.
4. `POST /api/v1/source-artifacts/{id}/complete` performs a TOS HEAD request and verifies size and the signed SHA256 metadata header.
5. `POST /api/v1/jobs` references `source.type=artifact` and `source.artifactId`.

Object keys are deterministic and tenant-scoped:

```text
tenants/{tenant-id}/users/{user-id}/sha256/{digest}.zip
```

Artifacts are immutable. Re-uploading the same digest for the same tenant reuses the existing object and returns an idempotent response.

### 5.2 Browser flow

The Create Job page adds a `Local package` source option. The browser follows the same create/upload/complete sequence using a selected ZIP file, then submits the job. Directory packing remains a CLI responsibility because browsers cannot reliably walk arbitrary local projects with ignore semantics.

### 5.3 TOS policy

- The bucket is private and has no anonymous access.
- Backend credentials can issue narrowly scoped pre-signed PUT/HEAD/GET operations only for tenant-prefixed object keys.
- A source-materializer initContainer downloads the immutable object, verifies SHA256 before extraction, rejects path traversal and escaping symlinks, and extracts into `/workspace`.
- Objects with no completed artifact record are removed after 7 days.
- Referenced objects are retained for at least 30 days after the last linked job, then lifecycle policy may remove them.

## 6. Ray 2.35.0 Jobs API Compatibility

The stock CLI supports `--address`, `--headers`, `--verify`, `--working-dir`, and `--metadata-json`. V1 implements the exact paths used by Ray 2.35.0:

- `GET /ray/api/version`
- `GET /ray/api/packages/{protocol}/{package_name}`
- `PUT /ray/api/packages/{protocol}/{package_name}`
- `POST /ray/api/jobs/`
- `GET /ray/api/jobs/`
- `GET /ray/api/jobs/{submission_id}`
- `POST /ray/api/jobs/{submission_id}/stop`
- `DELETE /ray/api/jobs/{submission_id}`
- `GET /ray/api/jobs/{submission_id}/logs`
- `GET /ray/api/jobs/{submission_id}/logs/tail`

Ray CLI authentication uses:

```bash
--headers '{"Authorization":"Bearer rpt_..."}'
--verify /path/to/ray-platform-ca.crt
```

Ray CLI uploads local packages with a PUT request. For compatibility, the gateway streams the request to TOS without buffering it in memory and enforces the same 2 GiB limit. The preferred `rayctl` path remains direct-to-TOS and avoids this proxy hop.

`POST /ray/api/jobs/` translates Ray's `JobSubmitRequest` into the existing platform TrainingJob. Platform cluster settings are read from Ray metadata keys:

- `ray-platform.image` — required immutable image digest;
- `ray-platform.worker-replicas` — default `1`;
- `ray-platform.gpus-per-worker` — default `1`;
- `ray-platform.cpu-per-worker` — default `8`;
- `ray-platform.memory-per-worker` — default `32Gi`;
- `ray-platform.queue` — defaults to the tenant LocalQueue.

The package URI uploaded by the Ray CLI maps to a tenant-owned source artifact. The compatibility route accepts only Ray 2.35 supported gcs package protocol, validates the package name as a canonical digest-based ZIP name, and never trusts it as an object-store key. The translated job is persisted before its RayJob CR is created, so it appears in the Portal immediately. Status and logs endpoints read from the same repository/reconciler used by browser-submitted jobs.

Direct access to a Ray Head service is not exposed. This prevents bypassing Kueue, quotas, image allowlists, audit logging, and tenant ownership.

## 7. API and Data Model

### 7.1 Database migrations

Add `personal_access_tokens`:

- `id`, `public_id`, `user_id`, `tenant_id`, `token_digest`, `scopes`, `expires_at`, `last_used_at`, `revoked_at`, `created_at`;
- unique index on `public_id`;
- indexes on owner and active expiry.

Add `source_artifacts`:

- `id`, `tenant_id`, `user_id`, `sha256`, `size_bytes`, `object_key`, `state`, `upload_expires_at`, `completed_at`, `last_referenced_at`, `created_at`;
- unique constraint on `(tenant_id, sha256)`;
- no pre-signed URL or credential is persisted.

Training jobs gain a nullable `source_artifact_id` foreign key. Existing `git`, `tos`, and `workspace` source records remain readable.

### 7.2 API behavior

- Create operations accept `Idempotency-Key`.
- Validation errors return 400 with stable error codes.
- Unauthorized and forbidden responses use 401 and 403 respectively.
- Artifact ownership mismatch is returned as 404 to avoid cross-tenant enumeration.
- Upload conflicts return 409.
- TOS or Kubernetes transient failures return 503 with `Retry-After`.
- User-facing errors never include pre-signed URLs, object keys from another tenant, credentials, or internal stack traces.

## 8. Private ALB, DNS, and TLS

### 8.1 ALB

- Provision a private `ALBInstance` in the cluster VPC using private subnets in at least two availability zones when available.
- Listen on HTTPS 443 and route to backend services over HTTP inside the cluster.
- Enable health checks, access logging, request ID propagation, and an ACL limited to approved VPC/VPN/private-line CIDRs.
- Route `/api/v1`, `/ray/api`, and `/workspace` before the `/` frontend catch-all.
- Configure WebSocket support and long timeouts for log tailing and Jupyter.
- Limit compatibility upload request size to 2 GiB; normal API JSON requests use a much smaller limit.

### 8.2 TLS

- Install cert-manager in its own namespace.
- Create a self-signed bootstrap issuer, a private CA certificate, and a CA issuer.
- Issue a server certificate for `ray-platform.internal` into Secret `ray-platform-tls`.
- Prefer VKE ALB native Secret certificate integration when supported by the installed controller. This feature must be verified during preflight because VKE documents it as a trial capability in some releases.
- Fallback: synchronize the issued certificate to Volcengine Certificate Center through a scoped deployment script and bind the returned certificate ID to the ALB listener. Private keys must not be printed or committed.

### 8.3 DNS and client trust

- Internal DNS maps `ray-platform.internal` to the ALB private VIP.
- Until internal DNS exists, a documented hosts-file entry may be used for test clients.
- The CA certificate is distributed through an authenticated admin download and the deployment artifact directory.
- Browser/OS trust installation and CLI `--verify` examples are documented for Linux, macOS, and Windows.

## 9. Frontend Changes

- Add a Personal Access Tokens page with create, list, copy-once, and revoke flows.
- Add a Local Package option to Create Job.
- Show artifact digest, size, upload progress, expiry, and validation errors.
- Mark submission origin as `portal`, `ray-cli`, or `api` in job details.
- Existing job list/detail/log pages display jobs from all submission origins without separate navigation.
- Replace GitHub-specific placeholders with generic Git or private GitLab wording, while server-side private Git credentials remain out of V1.

## 10. Security Controls

- PAT plaintext, TOS credentials, pre-signed URLs, TLS private keys, and future Git tokens are never logged.
- PAT comparisons are constant-time.
- Every artifact and job access checks both tenant and user/role authorization.
- Zip extraction enforces file-count, expanded-size, path, symlink, and compression-ratio limits.
- Job image must remain pinned by digest and pass the tenant image allowlist.
- Ray metadata is treated as untrusted input and schema-validated.
- ALB is private and CIDR-restricted; backend authentication remains mandatory even on the private network.
- Audit records cover PAT creation/revocation, artifact upload completion, job submission, cancellation, and deletion.
- API rate limits are applied per IP and principal, with tighter limits on PAT verification failures and upload URL creation.

## 11. Testing and Acceptance

### 11.1 Automated tests

- Unit tests for PAT parsing, hashing, scopes, expiry, and redaction.
- Repository integration tests for token and artifact tenant isolation.
- TOS signer tests using a fake object store interface.
- Archive security tests for traversal, symlink escape, zip bomb limits, wrong digest, and oversized input.
- Ray 2.35.0 contract tests instantiate the real `JobSubmissionClient` against the gateway.
- API tests cover OIDC and PAT authorization, idempotency, and stable status codes.
- Helm lint, template, and server-side dry-run validate ALB/cert-manager resources.
- Frontend component tests cover token copy-once behavior and upload state.

### 11.2 End-to-end acceptance

1. Install CA on a client reachable through the private network.
2. Open `https://ray-platform.internal` and create a PAT.
3. Submit a one-GPU smoke job with `rayctl` from a local project directory.
4. Confirm code uploads directly to TOS and the Portal shows the job immediately.
5. Confirm Kueue admits one GPU and the worker sees `CUDA_VISIBLE_DEVICES`.
6. Confirm job succeeds and logs contain `smoke_gpu_probe` and training metrics.
7. Submit the same program with stock Ray 2.35.0 CLI and required metadata.
8. Confirm the second job appears in the same Portal and supports status, logs, stop, and delete.
9. Revoke the PAT and confirm subsequent CLI access returns 401.

Coverage target is at least 80% for new backend packages and critical frontend/API flows.

## 12. Documentation Deliverables

- `docs/PRIVATE_ALB_DEPLOYMENT.md` — ALBInstance, Ingress, private subnet, ACL, health checks, DNS, and rollback.
- `docs/CERT_MANAGER_PRIVATE_CA.md` — cert-manager installation, CA issuance, ALB binding, renewal, client trust, and emergency rotation.
- `docs/USER_SUBMISSION_GUIDE.md` — Portal, PAT, `rayctl`, local packaging, ignore files, status, logs, and cancellation.
- `docs/RAY_CLI_COMPATIBILITY.md` — exact Ray 2.35.0 commands, metadata, headers, CA verification, and supported endpoints.
- `docs/TOS_SOURCE_ARTIFACTS.md` — bucket policy, lifecycle, object layout, limits, and recovery.
- `docs/TRAINING_E2E_RUNBOOK.md` — one-GPU smoke test, expected states, diagnostic commands, cleanup, and rollback.
- Update `docs/CLUSTER_DEPLOYMENT_GUIDE.md` with links and the production deployment sequence.

Each runbook includes prerequisites, exact commands, validation evidence, failure symptoms, rollback steps, and which operations create billable cloud resources.

## 13. Rollout Sequence

1. Add schema and repository support behind disabled feature flags.
2. Add PAT authentication and artifact APIs with tests.
3. Add source materializer artifact verification.
4. Add Ray 2.35.0 compatibility endpoints and real-client contract tests.
5. Add Portal PAT and local-package flows.
6. Add cert-manager and private ALB Helm resources.
7. Deploy backend/frontend immutable images.
8. Create the private TOS bucket and lifecycle policy.
9. Provision private ALB and issue TLS certificate.
10. Configure temporary hosts mapping, then internal DNS.
11. Run Portal, `rayctl`, and stock Ray CLI one-GPU acceptance tests.
12. Enable feature flags for intended tenants and publish the access/runbook documentation.

## 14. Non-goals for V1

- Public internet exposure.
- Direct user access to Ray Head port 8265.
- Storing per-user GitLab credentials.
- Automatically building arbitrary user Dockerfiles.
- Multi-version Ray CLI compatibility beyond 2.35.0.
- Replacing Kueue, KubeRay, Keycloak, or the current PostgreSQL repository.

## 15. External Inputs Required at Deployment Time

The deployment runbook discovers or requests these values without storing them in source control:

- private subnet IDs for the ALB zones;
- allowed client CIDRs for the ALB ACL;
- TOS endpoint, region, and scoped credential Secret name;
- PAT pepper Secret name;
- Keycloak production issuer/client/audience;
- internal DNS administrator action after the ALB VIP is allocated.

No plaintext credential is accepted in Helm values or committed documentation.

## 16. REAL TOS GATE

Unit tests and SDK-adapter tests are not a substitute for a real private TOS bucket. Deployment remains blocked until an operator validates all of the following against the intended bucket and scoped credential:

- a presigned PUT succeeds with browser-controlled `Content-Type`, `x-tos-meta-sha256`, `If-None-Match`, and `x-tos-forbid-overwrite`, while the user agent supplies the signed `Content-Length` from the original `File` or `Blob`;
- a second PUT to the same deterministic key fails with HTTP 412 because signed `If-None-Match: *` is the primary cross-versioning create-only guarantee;
- `x-tos-forbid-overwrite: true` is treated only as defense in depth because its behavior can differ when bucket versioning is enabled;
- HEAD returns the exact declared content length and lowercase `sha256` user metadata used by the completion API;
- both path-style and bucket-subdomain presigned hosts, if returned by the configured endpoint, pass the backend's strict host validation;
- credentials, security tokens, object keys from other owners, and presigned URLs do not appear in application or ingress logs.

Bucket versioning or object lock should be evaluated as additional recovery/immutability controls, but neither replaces the signed `If-None-Match: *` contract. Record the real request IDs, status codes, bucket policy/versioning state, and cleanup evidence in the deployment runbook without recording a presigned URL or credential.

## 17. References

- Volcengine VKE ALB Ingress: https://www.volcengine.com/docs/6460/100992?lang=en
- Volcengine ALB annotations: https://www.volcengine.com/docs/6460/132415?lang=zh
- Volcengine ALBInstance via kubectl: https://www.volcengine.com/docs/6460/1167729?lang=zh
- Runtime contract source: Ray 2.35.0 `ray.dashboard.modules.job.sdk`, `dashboard_sdk`, and `job_head` from the deployed immutable Ray image.
