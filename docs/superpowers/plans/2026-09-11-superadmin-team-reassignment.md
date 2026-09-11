# SuperAdmin Team Reassignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow SuperAdmin to create/rename teams and atomically move users between teams without moving personal objects, while preserving historical ownership and leaving all GPU quotas unchanged.

**Architecture:** Keep `local_users.tenant_id` as the immutable storage-home tenant and use `active_tenant_id` only for authorization. Replace legacy composite owner foreign keys with a non-authorizing historical ownership table, resolve all personal paths through a server-owned personal binding, and expose one audited atomic reassignment API. Add only the corresponding Portal management controls; retain backward-compatible old UI responses.

**Tech Stack:** Go 1.25, Gin, GORM, PostgreSQL migrations, Vue 3/Element Plus Portal, Kubernetes/Helm, GitLab CI.

---

### Task 1: Add the forward-compatible identity and ownership migration

**Files:**
- Create: `backend/db/migrations/0045_identity_storage_home.up.sql`
- Create: `backend/db/identity_storage_home_migration_test.go`
- Modify: `backend/db/postgres_test.go`
- Modify: `backend/db/postgres_integration_test.go`

- [ ] **Step 1: Write the migration contract test**

Add a test that reads migration 45 and requires `storage_tenant_id` on `data_mount_bindings`, the immutable `identity_tenant_ownerships` table, safe backfill from legacy owners, and replacement owner foreign keys referencing
`identity_tenant_ownerships(identity_id, tenant_id)`. Require migration version 45 in the embedded sequence.

- [ ] **Step 2: Run the focused test on the build host and verify RED**

Run the candidate tree in the fixed Go builder with:

```bash
go test ./db -run 'TestIdentityStorageHomeMigration|TestMigrationVersionsEmbedded' -count=1
```

Expected: failure because migration 45 is absent.

- [ ] **Step 3: Implement migration 45**

The migration must begin with the required lock and statement timeouts, add and backfill the binding storage-home column, create and backfill historical ownership from existing users/memberships/resources, drop only the four known `users(id, tenant_id)` constraints, and recreate them against `identity_tenant_ownerships`. Do not create memberships, grant roles, drop legacy columns or delete rows.

- [ ] **Step 4: Extend PostgreSQL integration assertions**

Assert that migration 45 applies twice idempotently, owner constraints reference the ownership table, active personal bindings have a storage-home tenant, and historical resources without memberships remain preserved without gaining an active membership.

- [ ] **Step 5: Run focused DB tests and verify GREEN**

```bash
go test ./db -run 'TestIdentityStorageHomeMigration|TestMigrationVersionsEmbedded|TestPostgresMigrationsIntegration' -count=1
```

Expected: PASS when PostgreSQL integration configuration is available; otherwise the integration test must report its existing explicit skip condition.

### Task 2: Make identity persistence membership-safe

**Files:**
- Modify: `backend/repositories/identity.go`
- Modify: `backend/repositories/local_auth.go`
- Modify: `backend/repositories/admin.go`
- Modify: `backend/repositories/identity_test.go`
- Modify: `backend/repositories/oauth2_proxy_accounts_test.go`
- Modify: `backend/repositories/personal_access_tokens_test.go`

- [ ] **Step 1: Write failing repository tests**

Cover an identity with home tenant `team-a`, active tenant `team-b`, an old `team-a` PAT and different membership roles. Call `EnsureIdentity` with the `team-b` principal and assert that the legacy/home tenant and old PAT remain unchanged while username/email update. Assert `ListUserSummaries` reports `team-b` and its membership roles.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
go test ./repositories -run 'TestEnsureIdentityPreservesStorageHomeAcrossTeamSwitch|TestListUserSummariesUsesActiveMembership' -count=1
```

Expected: failure because `EnsureIdentity` currently updates `users.tenant_id` and summaries read the legacy row.

- [ ] **Step 3: Implement stable identity persistence**

Expose `StorageTenantID` from the existing immutable `LocalUserRecord.TenantID`, and stop updating `UserRecord.TenantID` or `roles` on conflict. Update summaries to join the active membership and merge only global SuperAdmin role. Keep the legacy fallback for databases before migration 43.

- [ ] **Step 4: Run repository tests and verify GREEN**

```bash
go test ./repositories -run 'Identity|OAuth2Proxy|Membership|PersonalAccessToken|UserSummaries' -count=1
```

Expected: PASS, including the existing team-bound PAT regression test.

### Task 3: Preserve one personal root across active teams

**Files:**
- Modify: `backend/domain/data_mount_binding.go`
- Modify: `backend/domain/data_space.go`
- Modify: `backend/domain/source_artifact.go`
- Modify: `backend/domain/workspace_snapshot.go`
- Modify: `backend/repositories/data_spaces.go`
- Modify: `backend/api/data_spaces.go`
- Modify: `backend/api/source_artifacts.go`
- Modify: `backend/api/local_users.go`
- Test: corresponding `*_test.go` files in `backend/domain`, `backend/repositories`, and `backend/api`

- [ ] **Step 1: Write cross-team personal-root tests**

Create a user whose active tenant is `team-b` and storage-home tenant is `team-a`. Assert that data browsing, upload tickets, workspace snapshots, source archives, quota lookup and training data roots all resolve to
`ray-train/tenants/team-a/users/<storage_key>/`, while team-shared roots resolve to `team-b`.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test ./domain ./repositories ./api -run 'StorageHome|CrossTeamPersonal|PersonalRoot' -count=1
```

Expected: failure because validation currently requires the personal root tenant to equal the active tenant.

- [ ] **Step 3: Implement server-owned storage-home resolution**

Add `StorageTenantID` to personal bindings. Validate the object root against `(storage_tenant_id, storage_key)` while authorization continues to require `(active tenant, user ID)`. Construct per-namespace bindings from the account storage-home value; never accept either root component from HTTP input. Update personal quota and source/snapshot helpers to consume the resolved binding root rather than concatenate active tenant.

- [ ] **Step 4: Run all data-space and artifact tests**

```bash
go test ./domain ./repositories ./api -run 'DataSpace|DataMount|SourceArtifact|WorkspaceSnapshot|StorageQuota' -count=1
```

Expected: PASS with existing traversal, owner-isolation and no-secret tests unchanged.

### Task 4: Add atomic SuperAdmin reassignment and tenant rename APIs

**Files:**
- Modify: `backend/api/memberships.go`
- Modify: `backend/api/admin.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/repositories/memberships.go`
- Modify: `backend/repositories/admin.go`
- Modify: `backend/api/memberships_test.go`
- Modify: `backend/api/admin_quota_test.go`
- Modify: `backend/repositories/memberships_test.go`

- [ ] **Step 1: Write failing handler and transaction tests**

Require `PUT /api/v1/users/:id/active-membership` to reject non-SuperAdmin callers, retired teams, global-role injection and invalid identities. Require a successful request to upsert target roles, switch active tenant and optionally deactivate every other membership in one transaction. Inject a failure after switching and assert full rollback. Require `PATCH /api/v1/tenants/:id` to change only `name`.

- [ ] **Step 2: Run focused API/repository tests and verify RED**

```bash
go test ./api ./repositories -run 'AdminReassign|TenantDisplayName|AtomicMembership' -count=1
```

Expected: 404/missing method failures.

- [ ] **Step 3: Implement repository transactions**

Add `ReassignActiveMembership(ctx, identityID, tenantID string, roles []string, deactivateOthers bool)` and `SetTenantDisplayName(ctx, tenantID, name string)`. Use row/advisory locks in the existing order, parameterized ORM queries, validated roles, active-tenant fences and immutable updates. Do not update quotas, namespaces, queues or storage-home fields.

- [ ] **Step 4: Implement authorized handlers and audit payloads**

Register both routes under the existing authenticated API group. Require global SuperAdmin before body processing, cap display names, return stable error codes, and record old/new active tenant plus deactivated memberships without exposing storage roots or token data.

- [ ] **Step 5: Run focused tests and verify GREEN**

```bash
go test ./api ./repositories -run 'Membership|Reassign|TenantDisplayName|Admin' -count=1
```

Expected: PASS.

### Task 5: Add Portal team creation, rename and reassignment controls

**Files:**
- Modify: `src/views/rayTrain/api/memberships.js`
- Modify: `src/views/rayTrain/api/catalog.js` or the active tenant API module
- Modify: the active SuperAdmin user/team management view under `src/views/rayTrain/`
- Test: the colocated RayTrain API/component contract tests used by the Portal repository

- [ ] **Step 1: Write failing Portal contract tests**

Assert exact methods/paths for tenant creation, tenant display-name patch and active-membership reassignment. Assert the dialog shows target team, roles, “移出其他团队”, personal-data preservation, historical-team-data retention and old-PAT invalidation warnings. Assert controls are absent for non-SuperAdmin.

- [ ] **Step 2: Run the Portal lint image on the build host and verify RED**

```bash
docker build --pull -f docker/Dockerfile.lint .
```

Expected: the new contract test fails before implementation.

- [ ] **Step 3: Implement API helpers and management dialogs**

Use the existing authenticated request client and `error.message` renderer. Submit only stable IDs, normalized roles and the boolean replacement choice. Refresh server state after success; do not mutate cached users or tenants in place. Disable submit while pending and require confirmation when old memberships will be deactivated.

- [ ] **Step 4: Run the full Portal lint image and verify GREEN**

```bash
docker build --pull -f docker/Dockerfile.lint .
```

Expected: `lint:check`, `check:ep`, `check:store` and the repository tests PASS.

### Task 6: Full backend verification and security review

**Files:**
- Modify only if a test or review finds an in-scope defect.

- [ ] **Step 1: Run the complete backend suite on the build host**

Use the pinned Go builder command from `.agents/skills/release/SKILL.md` and run:

```bash
go test -timeout=20m ./...
```

Expected: all packages PASS.

- [ ] **Step 2: Review authorization and input boundaries**

Verify global SuperAdmin is checked server-side; all identifiers are parameterized; roots, CSI data and credentials are never accepted or returned; membership deactivation invalidates PAT authentication; audit output contains no token digest; and no secret appears in the diff.

- [ ] **Step 3: Review the complete diff**

```bash
git diff --check
git diff --stat <base>...HEAD
git diff <base>...HEAD
```

Expected: only the planned backend/docs and Portal RayTrain files changed.

### Task 7: Publish backend and Portal without touching training workloads

**Files:**
- Create on build host: a minimal backend image override YAML and retained values/manifest backups.

- [ ] **Step 1: Commit the verified backend candidate as the platform identity**

```bash
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit -m "feat: support safe team reassignment"
```

- [ ] **Step 2: Bundle-test, push both remotes and fast-forward the build host**

Follow the release skill exactly. Verify local, GitHub, internal GitLab and build-host full SHA equality and clean worktrees.

- [ ] **Step 3: Build only the backend image**

```bash
BUILD_TARGETS=backend PUSH_IMAGE=true USE_BUILDX=true BUILD_PLATFORM=linux/amd64 bash build-image.sh
```

Record the Harbor digest; do not build training, workspace, dataset or CLI images.

- [ ] **Step 4: Preserve runtime state and perform Helm dry-run diff**

Record active RayJob/RayCluster/Workload UIDs and back up release values/manifest. Use `--reuse-values` plus a backend-only digest override. The server-side diff must contain only the backend image change.

- [ ] **Step 5: Deploy the backend atomically and verify**

Use `helm upgrade --atomic --wait`. Verify rollout, migration 45, imageID, health, 401 (not 404) on new unauthenticated routes, no panic/migration errors, and unchanged training-resource UIDs.

- [ ] **Step 6: Push the verified Portal commit as guofeng.su**

Push `dev` with `--no-verify` only after the build-host lint image passes. Verify GitLab CI and the deployed SuperAdmin page; do not build or deploy the old frontend.

### Task 8: Apply and verify the requested production changes

**Files:**
- No source changes.

- [ ] **Step 1: Snapshot affected records**

Read and retain the `local` tenant summary, both users, memberships, PAT metadata, personal bindings, object counts and storage quota metadata. Confirm `local=24`, `algorithm=8`, `devops=0` immediately before the write.

- [ ] **Step 2: Rename only the local display name**

Call the authenticated SuperAdmin tenant-name API with `感知应用算法团队`. Re-read and assert stable `id=local`, namespace, queue, accelerator and quota 24.

- [ ] **Step 3: Atomically reassign both users**

For `zihao.liu` and `xin.gong`, call the SuperAdmin active-membership API with target `devops`, role `Engineer` and `deactivateOtherMemberships=true`. Stop if the first user's postconditions fail; do not batch opaque SQL updates.

- [ ] **Step 4: Verify data and authorization postconditions**

Assert each user has active/current `devops`, inactive `local`, unchanged `storage_key`, unchanged storage-home tenant and unchanged personal root/object counts. Verify the old local PAT no longer authenticates and no PAT row was deleted.

- [ ] **Step 5: Verify quota and workload invariants**

Assert tenant quotas remain `24/8/0`, active training UIDs remain unchanged, and a devops GPU submission is rejected with the documented zero-quota error rather than an internal error.

- [ ] **Step 6: Report exact versions and behavior**

Report backend four-end SHA, Portal SHA/CI, Harbor digest, Helm revision, affected membership states, display-name result, unchanged quotas and any acceptance step that could not be performed without the users' OAuth sessions.
