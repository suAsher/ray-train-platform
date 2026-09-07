# Admin queue and GPU capacity recovery — release record

## Causes and fixes

- Admin queue omitted `scope=team`, defaulting to the current user's jobs.
  Explicit administrator scope, complete pagination, unavailable/stale states
  and disabled stale actions now preserve the existing backend role boundary.
- Quota UI called the runtime training ceiling physical cluster capacity.
  It now separates topology GPUs, training submission capacity and assigned team
  budgets; unavailable measurements are not replaced by 0 or default 16.
- Initial `ProcessOnce` cleanup errors terminated the elected reconciler while
  its leader-election callback kept the lease. Runtime evidence showed the
  leader acquiring the lease at 03:27:04 UTC and immediately encountering two
  historical cleanup constraint errors. The initial cycle now retries like all
  subsequent cycles; tests cover transient and persistent errors plus 16→24.
- Process-local resource limits were refreshed only on the leader. Each API
  replica now reads capacity initially and every 5 seconds with a read timeout;
  only the leader writes Kueue. Read failures retain the last valid limits.

## Deployment and verification

- Frontend `1c0289c`, tag `release-20260907-02`, Helm revision 178:
  `sha256:a5f34e4d3c5f6cc9d81a3a320e6386b6bf4bc4378df39a56208244f5aaf39235`.
- Backend `c14b8e4`, tag `release-20260907-03`, Helm revision 179:
  `sha256:a57eddb6dc70afe7e2dd70ce3d9887d15c8a67a56afbc4a84e97bb941872a405`.
- Each server dry-run changed exactly its component image line. Atomic upgrades
  completed; frontend/backend/CLI all 2/2 available; CLI image unchanged.
- Frontend 362 tests and build passed; backend all-package tests/build passed;
  observer targeted race tests and node-script contracts passed. No migration.
- Both backend replicas logged `nodes=3 GPUs=24 GPUsPerWorker=8`.
  Leader logged automatic Kueue sync at 07:08:58 UTC; ClusterQueue GPU quota
  became 24, CPU 532023m, memory 2213411723072. No manual Kueue patch was used.
- UI/health returned 200. Existing training job
  `job-29dc380420222684984b87cf` remained RUNNING; its head, worker and submitter
  UIDs stayed unchanged with zero restarts.
- One frontend Pod scheduled to the new node restarted twice during startup
  because backend service DNS initially failed; it subsequently became Ready.
  This was not a training Pod restart.

## Remaining boundaries

- The user confirmed manually uncordoning 172.28.1.229 during frontend rollout.
  The release did not uncordon it or prepare its host directories. Both cache
  mappings still omit it; `/data1/ray-cache` and `/data2/ray-cache` were absent.
  A 24-GPU admission budget does not prove cache/data-path training readiness.
- Historical managed-attempt cleanup constraints still fail. Their failures no
  longer kill the control loop; cleanup correctness remains a separate issue.
- Node onboarding wizard scope is approved; its separate design awaits review.
  No wizard runtime, node-writing privileges or admission policies were deployed.
- Values/manifest backups and exact image overrides remain on the build host.
  Temporary release bundles and dry-run files were removed.
