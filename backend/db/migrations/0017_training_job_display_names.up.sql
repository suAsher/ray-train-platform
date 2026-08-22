-- A training job's immutable platform ID is its identity. `name` is a display
-- label that users intentionally reuse while iterating on the same project.
ALTER TABLE training_jobs
  DROP CONSTRAINT IF EXISTS training_jobs_tenant_id_name_key;

CREATE INDEX IF NOT EXISTS training_jobs_tenant_name_idx
  ON training_jobs (tenant_id, name);
