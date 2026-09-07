# Failed publication cleanup and retired account removal

Approved scope: administrators can remove failed publication/version records and
provably exclusive outputs; preserve source/shared objects, all training history,
checkpoints and personal files. SuperAdmin can decommission retired-team accounts.
No production data deletion or deployment is part of development verification.

Implementation and verification:

1. Add governance single/batch cleanup with explicit irreversible confirmation,
   partial-failure reporting and unchanged role boundaries.
2. Add a separate permanent-cleanup API; never silently reinterpret the existing
   record-only delete API. Lock and revalidate failed records, reject references,
   active leases and live publisher Pods/Jobs. Fail closed on unknown state.
3. Delete only server-derived version manifest / version publication / run-temp
   paths. Shared content-addressed shards and receipts are not proven exclusive
   merely by missing database references and must remain untouched.
4. Remove failed records after successful object cleanup. Retain minimal identity
   fences (not publication history) to reject stale retries/recreation.
5. Separate admin account lookup/decommission from authentication; keep retired
   account login/reactivation forbidden. Preserve user history and files.
6. Test negative permission/path/reference/lease cases, storage partial failure,
   stale writers and PostgreSQL migration; then run backend/frontend suites.

Open acceptance boundary: content-addressed orphan collection requires a complete
reference/receipt inventory plus writer fencing; it is not safe to infer from the
UI's zero object/byte counters. Report retained shared/unknown content explicitly.

## Development verification

- Governance and dataset pages expose separate permanent cleanup, single and
  batch confirmation, administrator scope checks and partial-failure reporting.
- New `/purge` does not fall back to record-only deletion when dependencies are
  absent. Missing/mismatched target bucket or endpoint keeps cleanup disabled.
- Backend all-package tests and build passed. Frontend 350 tests and build
  passed (existing bundle-size warning). Object-store tests cover wrong scope,
  listing errors/cycles, cancellation, delete failure and retry.
- Retired-user changed-function coverage: API 95.8%, repository lookup 100%,
  repository cleanup 94.4% after negative/concurrency-state tests.
- After explicit user permission, real PostgreSQL 16 verification passed on
  2026-09-07 in a network-isolated, resource-limited temporary Docker container.
  Tests covered all migrations through 36 (including repeat application),
  monotonic retired-account cleanup, active training writes, failed version and
  child-row purge, shared writer exclusion and stale identity recreation denial.
  The first run exposed an outdated migration expectation of 34 in the PG-only
  integration test; corrected to 36 and rerun successfully. No production DB
  was used; the temporary container was removed after each run.
- Final implementation commit `56bb97d` also rejects active publisher resources
  with missing identity labels; the regression test was verified red then green.

## Production release — 2026-09-07

- User explicitly authorized source bundle transfer and API/UI deployment while
  training runs, without changing training resources. Source was synchronized
  via git bundle to the canonical build checkout.
- Built backend/frontend from `56bb97d`, tag `release-20260907-01`.
  Build-container downloads stalled; the same download worked from the host.
  A build-only `docker buildx build --network host` resolved this without source,
  Dockerfile, Kubernetes network or training configuration changes.
- Backend digest:
  `sha256:aa9f6ce5e21b0353c70e328c701e29d8179d79bda1863caf78d8c148669f714d`.
- Frontend digest:
  `sha256:04d0976a274e83874ca101e58f3655e78dc40aac8946d96802ca26b026055000`.
- Server dry-run manifest diff contained exactly the two image changes.
  Helm atomic upgrade completed: revision **177**, status **deployed**.
  Both components have 2/2 ready updated replicas, zero restarts, and imageIDs
  matching the registry digests. CLI deployment/image was unchanged.
- Production schema migration version is **36**. Public UI and healthz return
  200; unauthenticated permanent-cleanup request returns 401.
- Running job `job-29dc380420222684984b87cf` remains RUNNING. Head, worker and
  submitter Pod UIDs are unchanged and all restart counts remain zero.
- No production publication/account was deleted during verification. Admins
  must confirm actual cleanup in the UI; shared/unknown objects remain protected.
- Separate log observation: retirement attempts for jobs
  `job-722620455425b65340574d98` and `job-66808647523a42b4726f013a` hit a check
  constraint. Read-only DB inspection shows both ledgers already CLEANED with
  zero failures and no stored error; neither RayJob exists. Their retirement
  implementation was unchanged in this release. This log issue remains for
  separate investigation; no ledger or training resource was modified.
- Release values/manifest backups and the exact image override remain under
  `/root/` on the build host; temporary release bundle/dry-run files are removed.
