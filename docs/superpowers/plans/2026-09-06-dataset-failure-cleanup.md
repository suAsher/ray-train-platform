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

Real PostgreSQL validation was completed before release in a disposable, network-disabled, tmpfs-only container on the build host using `scripts/test-dataset-cleanup-postgres.py`: all33 migrations, tombstone audit/immutability, retained identity, attempt rejection, atomic rollback, and two-session NOWAIT55P03 without deadlock passed. This supplements the Go SQLite repository/API tests; it is SQL-layer rather than Go-on-PostgreSQL integration coverage. Safety checks use persisted states/leases, not Kubernetes inspection; stray worker resources are neither stopped nor removed.

## Release — 2026-09-06 17:31 CST

User approved deployment, preserving the existing running training. Helm release `ray-platform`, namespace `ray-train-platform`, revision **175**, `release-20260906-03`.

- Application image source: `2fc5f98566913cab1d46de0bc6383089822b0fe7`.
- PostgreSQL verification script commit: `6e8507b21c603ce40ef89bf66a6e6f344880df66`; no application source changed after image build.
- Backend: `sha256:8e216a3d3eebcd1880fc1c99a640bdffe584cc110345238a037360b3a540e705`.
- Frontend: `sha256:bcdd9742d8a3ae324bd7f670c164e06d8e1a67eb650e7f72f5f74a283759707e`.
- CLI, dataset publisher, training images and training resources were not updated.
- Frontend builder pinned to Node22 digest `sha256:a73e7081874832dc455788ba110e31d1278f2352c043e4191f34093d4d7da60e`; production dependency audit reported0 vulnerabilities.
- Server dry-run differed only in the two application image lines; upgrade used `--reuse-values --atomic --wait`.
- All four API/UI pods Ready with expected imageIDs; migration33 applied; healthz200; both new DELETE routes returned401 without credentials; deployed JS contains cleanup UI.
- Existing `tenant-local/job-29dc380420222684984b87cf` stayed RUNNING with identical UID/spec and head/worker Pod UIDs/restart counts.
- No existing production records deleted: dataset versions remained7FAILED+1READY. Soft deletion is administrator-triggered and does not reclaim storage.
- Authenticated production API/UI acceptance was not performed: automatic extraction of bootstrap administrator credentials was denied by safety review. Explicit authorization is required before using those credentials; do not bypass this restriction. Role/scope behavior was tested locally, and PostgreSQL guards in the isolated instance.
- Private backups/evidence: `/root/dataset-cleanup-release-20260906-03/` (values, database dump, override, build/upgrade/PostgreSQL logs and training snapshots). Temporary test container and transport/dry-run files are removed after verification; backups retained.
