-- training_jobs is a compact control-plane table; this migration fails and
-- rolls back instead of waiting indefinitely for locks or index creation.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS training_engine TEXT NOT NULL DEFAULT 'ray-ddp';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS ray_version TEXT NOT NULL DEFAULT '2.35.0';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS cluster_attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS worker_restart_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS resume_checkpoint_id TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS parent_job_id TEXT NOT NULL DEFAULT '';

ALTER TABLE training_jobs
  ADD CONSTRAINT training_jobs_training_engine_check CHECK (training_engine IN ('ray-ddp', 'ray-train')),
  ADD CONSTRAINT training_jobs_ray_version_check CHECK (ray_version IN ('2.35.0', '2.56.1', '2.58.0')),
  ADD CONSTRAINT training_jobs_engine_ray_version_check CHECK (training_engine <> 'ray-train' OR ray_version <> '2.35.0'),
  ADD CONSTRAINT training_jobs_cluster_attempt_check CHECK (cluster_attempt >= 1),
  ADD CONSTRAINT training_jobs_worker_restart_count_check CHECK (worker_restart_count >= 0),
  ADD CONSTRAINT training_jobs_parent_job_id_check CHECK (parent_job_id = '' OR parent_job_id ~ '^job-[0-9a-f]{24}$');

CREATE INDEX IF NOT EXISTS training_jobs_engine_state_idx
  ON training_jobs(training_engine, observed_state);
CREATE INDEX IF NOT EXISTS training_jobs_parent_idx
  ON training_jobs(parent_job_id) WHERE parent_job_id <> '';
