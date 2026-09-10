SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE idc_sync_connectors
  ADD COLUMN IF NOT EXISTS sync_interval_minutes INTEGER NOT NULL DEFAULT 0;

ALTER TABLE idc_sync_connectors
  DROP CONSTRAINT IF EXISTS idc_sync_connectors_interval_check;

ALTER TABLE idc_sync_connectors
  ADD CONSTRAINT idc_sync_connectors_interval_check
  CHECK (sync_interval_minutes = 0 OR sync_interval_minutes BETWEEN 5 AND 10080);
