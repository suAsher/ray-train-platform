ALTER TABLE training_jobs
  ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS training_jobs_visible_created_idx
  ON training_jobs(tenant_id, created_at DESC)
  WHERE archived_at IS NULL;
