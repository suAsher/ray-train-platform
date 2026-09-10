SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE idc_sync_runs
  ADD COLUMN IF NOT EXISTS tombstoned_object_count BIGINT NOT NULL DEFAULT 0;

ALTER TABLE idc_sync_runs
  DROP CONSTRAINT IF EXISTS idc_sync_runs_tombstoned_count_check;

ALTER TABLE idc_sync_runs
  ADD CONSTRAINT idc_sync_runs_tombstoned_count_check
  CHECK (tombstoned_object_count >= 0);
