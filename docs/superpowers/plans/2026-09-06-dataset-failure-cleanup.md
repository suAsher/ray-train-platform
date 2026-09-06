# Admin Dataset Failure Cleanup Implementation Plan

**Goal:** Let administrators remove failed version/publication records from the catalogue without deleting source files, Parquet objects or training resources.

**Architecture:** Add deletion timestamp/actor to version and publication metadata; retain immutable identities and audit data. Transactional checks enforce owning-tenant scope, FAILED state, absence of active publication work and absence of all training references. Frontend hides failures by default and exposes explicit confirmed single/batch deletion only for authorized administrators.

## Contract and safety

- `DELETE /api/v1/datasets/:id/versions/:versionID`: atomically hide failed unreferenced version and failed publication records.
- `DELETE /api/v1/datasets/:id/versions/:versionID/publication`: hide failed publication record, leaving version intact.
- Both return `{success:true,data:{deleted:true}}`; missing/invisible404, safety conflict409, unauthorized401/403, unavailable503, rate limit429.
- SuperAdmin may manage all; TenantAdmin may manage own TEAM datasets only. PUBLIC mutation is SuperAdmin-only. Engineer and PAT sessions denied even if PAT carries administrator roles.
- Soft deletion is not storage reclamation. Keep source files, manifests, shards, provenance, immutable IDs and unique constraints. No remote cleanup jobs or training mutations.
- Batch frontend performs explicit chosen IDs sequentially, maximum100; report partial success and per-ID failure reasons, preserve failed selection for retry.

## Tasks

- [x] Repository tests for deletion lifecycle, scope, live work, referenced versions, late callbacks and transaction rollback; migration0033, models, guarded transaction and filtered lookups.
- [x] API tests for both routes, roles/auth types, real-store scope, error mapping and rate limit; interactive management routes with independent role enforcement.
- [x] UI helper tests for failed filtering and partial batch results; show-failed counts and admin-only actions, preserved READY training actions.
- [x] Admin documentation states metadata-only soft deletion and storage limitations.
- [x] Independent safety/concurrency review and local mocked-browser workflow: hidden failures, administrator confirmation, partial conflict preserving selection, ordinary users without deletion controls. Contention fix uses NOWAIT on attempts and maps PostgreSQL 55P03/40P01 to409.

## Validation and release boundary

Backend full tests and build passed; frontend328 tests and production build passed. Helper coverage:100% lines/functions,94.44% branches. Browser used simulated responses only; no production data changed.

Real PostgreSQL migration0033 trigger execution and concurrent worker/cleanup behavior remain unverified: this machine has no PostgreSQL runtime. Verify in an isolated PostgreSQL environment before production rollout. SQLite tests do not prove PostgreSQL locking or trigger behavior. Safety checks use persisted states/leases, not Kubernetes inspection; stray worker resources are neither stopped nor removed.

No deployment, production record cleanup, or storage reclamation was performed in this task.
