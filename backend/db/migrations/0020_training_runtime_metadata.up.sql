ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS training_engine TEXT NOT NULL DEFAULT 'ray-ddp';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS ray_version TEXT NOT NULL DEFAULT '2.35.0';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS cluster_attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS worker_restart_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS resume_checkpoint_id TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS parent_job_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS training_jobs_engine_state_idx
  ON training_jobs(training_engine, observed_state);
CREATE INDEX IF NOT EXISTS training_jobs_parent_idx
  ON training_jobs(parent_job_id) WHERE parent_job_id <> '';
