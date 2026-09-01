-- Execution mode is immutable per publication request. Existing in-flight
-- runs are legacy so an upgrade cannot reinterpret PACKING as distributed
-- finalization.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE dataset_publication_runs
  ADD COLUMN IF NOT EXISTS execution_mode TEXT NOT NULL DEFAULT 'legacy';

ALTER TABLE dataset_publication_runs
  DROP CONSTRAINT IF EXISTS dataset_publication_runs_execution_mode_check;

ALTER TABLE dataset_publication_runs
  ADD CONSTRAINT dataset_publication_runs_execution_mode_check
  CHECK (execution_mode IN ('legacy', 'distributed'));
