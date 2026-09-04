-- Terminal RayJob resources were never deleted, so their submitter Job and Pod
-- accumulated indefinitely. This column records that a job's Kubernetes objects
-- have been retired so the sweep is idempotent and never re-queues a job it has
-- already cleaned. It is deliberately separate from archived_at, which hides a
-- job from listings and says nothing about cluster resources.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE training_jobs
  ADD COLUMN IF NOT EXISTS ray_job_retired_at TIMESTAMPTZ;

-- The sweep scans for terminal jobs that are old enough and not yet retired.
-- A partial index keeps that scan proportional to the outstanding backlog
-- rather than to the full job history, which is retained forever.
CREATE INDEX IF NOT EXISTS training_jobs_rayjob_retirement_idx
  ON training_jobs(observed_state, finished_at)
  WHERE ray_job_retired_at IS NULL;
