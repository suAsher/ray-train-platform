# Platform Governance Rollout Implementation Plan

> **For Codex:** Execute this plan incrementally. Every schema, auth, scheduling, and routing change must have a failing test before implementation and a build-host verification before release.

**Goal:** Deliver OAuth-backed multi-team identity, team-bound credentials, topology-aware GPU placement, accelerator classes, safe priorities/preemption, scheduling explanations, and the Portal controls needed to operate them without disrupting admitted or running Ray workloads.

**Architecture:** Preserve `local_users` as the stable identity/account table and add `tenant_memberships` plus an active-team pointer. Browser requests resolve the selected active membership on every OAuth2 Proxy request; PATs remain permanently bound to the membership selected at issuance. Scheduling adds an immutable scheduling contract to each job, translates legacy queue metadata server-side, and renders Kueue TAS and workload-priority labels. The current shared ClusterQueue remains the accounting boundary during the live migration; tenant queues are provisioned in shadow state and only become the submission target after the legacy queue has no admitted workloads.

**Tech Stack:** Go, Gin, GORM/PostgreSQL migrations, Kubernetes dynamic client, Kueue v0.19 v1beta2, KubeRay v1.6.2, Helm, Vue 3/Element Plus.

---

### Task 1: Add a backward-compatible membership schema

**Files:**
- Create: `backend/db/migrations/0043_tenant_memberships.up.sql`
- Create: `backend/domain/membership.go`
- Create: `backend/repositories/memberships.go`
- Test: `backend/db/tenant_membership_migration_test.go`
- Test: `backend/repositories/memberships_test.go`
- Modify: `backend/repositories/local_auth.go`

1. Write migration assertions for the new table, active tenant pointer, unique membership key, active-status check, role JSON, and a backfill from every live `local_users` row.
2. Write repository tests for listing memberships, switching only to an active membership, preventing removal of the last active membership, and preserving the stable storage key.
3. Add the migration and immutable domain value objects.
4. Resolve OAuth accounts from the selected membership; retain `SuperAdmin` as a global role when it exists on the legacy account.
5. Run the database/domain/repository tests on the build host.

### Task 2: Expose current-team and membership administration APIs

**Files:**
- Create: `backend/api/memberships.go`
- Test: `backend/api/memberships_test.go`
- Modify: `backend/api/session.go`
- Modify: `backend/api/admin.go`
- Modify: `backend/main.go`

1. Write handler tests for `GET /api/v1/me/memberships`, `POST /api/v1/me/active-tenant`, and administrator membership create/update/deactivate operations.
2. Require an interactive identity for switching, TenantAdmin for members of its own team, and SuperAdmin for cross-team mutations.
3. Derive tenant and roles entirely from the stored active membership; never accept a tenant override for ordinary resource writes.
4. Return membership and active-team data in the existing response envelope.
5. Run targeted API tests on the build host.

### Task 3: Keep PATs team-bound across browser team switches

**Files:**
- Modify: `backend/repositories/personal_access_tokens.go`
- Modify: `backend/api/personal_access_tokens.go`
- Test: `backend/repositories/personal_access_tokens_test.go`
- Test: `backend/api/personal_access_tokens_test.go`

1. Add a failing test proving a PAT issued for team A remains team A after the user selects team B.
2. Authorize the PAT owner through `local_users` and the PAT's active membership rather than the mutable `users.tenant_id` compatibility column.
3. Reject revoked/inactive memberships and retain existing scope enforcement.
4. Run targeted PAT tests on the build host.

### Task 4: Add the job scheduling contract

**Files:**
- Modify: `backend/domain/training_job.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/repositories/jobs.go`
- Create: `backend/domain/scheduling.go`
- Test: `backend/domain/scheduling_test.go`
- Test: `backend/api/submission_service_test.go`
- Test: `backend/repositories/jobs_record_test.go`

1. Define supported accelerator classes (`rtx4090`, `a100`, `a800`, `h20`) and priorities (`production`, `normal`, `opportunistic`).
2. Default old submissions to `rtx4090/normal/non-preemptible`; accept the historical `<tenant>-gpu` queue and rewrite it to the currently active queue selected by the server.
3. Allow `opportunistic` only when the job explicitly enables preemption and managed checkpoint recovery.
4. Persist the normalized contract inside `spec_json` so retries and recovery attempts cannot drift.
5. Run domain, submission, and repository tests on the build host.

### Task 5: Render real Kueue TAS and workload priority

**Files:**
- Modify: `backend/k8s/rayjob.go`
- Test: `backend/k8s/rayjob_test.go`
- Test: `backend/k8s/rayjob_managed_test.go`
- Modify: `helm/ray-train-platform/templates/kueue-resources.yaml`
- Modify: `helm/ray-train-platform/values.yaml`
- Test: `backend/config/*chart_test.go`

1. Assert worker PodTemplates carry `kueue.x-k8s.io/podset-preferred-topology=kubernetes.io/hostname`, RayJobs carry the mapped `kueue.x-k8s.io/priority-class`, and accelerator labels are server-generated.
2. Add the `Topology` object, attach `topologyName` to each ResourceFlavor, and create four WorkloadPriorityClasses. Keep production and normal equal; make opportunistic lower so only explicitly checkpointable opportunistic jobs can be victims.
3. Configure the shared ClusterQueue with `withinClusterQueue: LowerPriority`; do not enable cross-team reclaim during the shared-queue stage.
4. Render all accelerator flavors with zero quota for absent hardware and update quotas only from Ready, schedulable, correctly-labelled nodes.
5. Run Kubernetes and Helm tests on the build host and perform server-side dry runs against the deployed CRDs.

### Task 6: Add scheduling capacity and explanation APIs

**Files:**
- Create: `backend/k8s/scheduling_inventory.go`
- Create: `backend/api/scheduling.go`
- Test: `backend/k8s/scheduling_inventory_test.go`
- Test: `backend/api/scheduling_test.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/main.go`

1. Expose accelerator capacity grouped by stable accelerator class without leaking node credentials or unrelated pod data.
2. Explain Pending state from the persisted contract plus Kueue Workload conditions: tenant quota, cluster quota, accelerator absence, topology fragmentation, or priority wait.
3. Restrict global capacity details to administrators; allow job owners to read their own job explanation.
4. Run targeted tests on the build host.

### Task 7: Provision tenant queue resources without live over-admission

**Files:**
- Create: `backend/k8s/tenant_queue.go`
- Test: `backend/k8s/tenant_queue_test.go`
- Modify: `backend/api/admin.go`
- Modify: `backend/repositories/identity.go`
- Modify: `helm/ray-train-platform/values.yaml`

1. Create tenant/accelerator ClusterQueues in `Hold` shadow state, cohorts grouped by accelerator class, and corresponding LocalQueues.
2. Set nominal/borrowing/lending quotas from the platform database, never from request-body queue names.
3. Add a preflight that refuses activation while the legacy ClusterQueue has admitted workloads.
4. Keep existing tenants and running jobs on the legacy queue until the preflight is clean; activating the v2 strategy changes only future normalized submissions.
5. Run dynamic-client tests and a production server-side dry run.

### Task 8: Add Portal team and scheduling controls

**Files:**
- Create: `src/views/rayTrain/api/memberships.js`
- Create: `src/views/rayTrain/api/scheduling.js`
- Modify: `src/views/rayTrain/stores/session.js`
- Modify: `src/views/rayTrain/AccountSecurity/index.vue`
- Modify: `src/views/rayTrain/QuotaManage/index.vue`
- Modify: `src/views/rayTrain/components/admin/TenantPanel.vue`
- Modify: `src/views/rayTrain/components/admin/UserPanel.vue`
- Modify: `src/views/rayTrain/components/admin/QueuePanel.vue`
- Modify: `src/views/rayTrain/Job/CreateJob.vue`
- Modify: `src/views/rayTrain/Job/JobDetail.vue`
- Test: adjacent `*.test.js` files under `src/views/rayTrain`

1. Add tests for team selection, role refresh, team-bound PAT wording, accelerator/priority validation, and Pending explanations.
2. Add an account-level team switcher and administrator membership editor.
3. Add accelerator and priority selectors; only show the preemptible option for managed checkpoint-capable jobs.
4. Display the backend scheduling explanation on job detail and queue administration pages.
5. Run Portal unit, lint, EP residual, type/build checks through GitLab CI after push.

### Task 9: Formal Portal routing and staged production release

**Files:**
- Modify: `deploy/portal/common-raytrain-ingress.yaml`
- Modify: `.claude/skills/release/SKILL.md`
- Test: `deploy/portal/*` manifest validation and live route probes

1. Back up both common and test-dev ingress objects and save RayJob/RayCluster UID snapshots.
2. Apply only the exact Portal backend paths `/raytrain/api`, `/raytrain/ray`, and `/raytrain/mlflow`; do not add a broad catch-all and do not redirect `raytrain.wellspiking.ai` yet.
3. Run the full backend suite in a detached build-host worktree, push GitHub and internal GitLab as `guofeng.su`, then fast-forward the build checkout.
4. Build only the backend image, inspect its digest, server-side dry-run the minimal Helm overlay, and deploy atomically.
5. Push Portal `dev`, wait for GitLab CI, and validate OAuth identity, team switch, PAT binding, current/other-team resource isolation, TAS fields on a dry-run RayJob, and route health.
6. Compare pre/post running RayJob and RayCluster names and UIDs. Roll back the individual layer if identity, route, or workload invariants fail.
