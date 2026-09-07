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
- Implementation commit `2cccc9c` exists on main. Production is still revision
  176; no production data was deleted and this change is not yet deployed.
  Approval review requires separate explicit authorization to transfer the
  source bundle to the build host; that authorization has been requested.
