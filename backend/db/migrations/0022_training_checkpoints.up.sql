-- Managed event ingestion is on the training hot path. Bound lock waits so a
-- deployment rolls back instead of stalling every backend replica.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS training_checkpoints (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  epoch BIGINT NOT NULL DEFAULT 0,
  step BIGINT NOT NULL DEFAULT 0,
  object_path TEXT NOT NULL,
  metric_name TEXT NOT NULL DEFAULT '',
  metric_value DOUBLE PRECISION,
  complete BOOLEAN NOT NULL DEFAULT FALSE,
  is_best BOOLEAN NOT NULL DEFAULT FALSE,
  manifest_sha256 TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT training_checkpoints_id_check CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
  CONSTRAINT training_checkpoints_owner_check CHECK (tenant_id <> '' AND user_id <> ''),
  CONSTRAINT training_checkpoints_progress_check CHECK (epoch BETWEEN 0 AND 1000000000000 AND step BETWEEN 0 AND 1000000000000),
  CONSTRAINT training_checkpoints_path_check CHECK (length(object_path) BETWEEN 1 AND 4096),
  CONSTRAINT training_checkpoints_metric_check CHECK (
    length(metric_name) <= 128
    AND metric_value <> 'NaN'::DOUBLE PRECISION
    AND metric_value > '-Infinity'::DOUBLE PRECISION
    AND metric_value < 'Infinity'::DOUBLE PRECISION
  ),
  CONSTRAINT training_checkpoints_manifest_check CHECK (complete = FALSE OR manifest_sha256 ~ '^[0-9a-f]{64}$')
);
CREATE INDEX IF NOT EXISTS training_checkpoints_owner_job_idx
  ON training_checkpoints(tenant_id, user_id, job_id, epoch DESC, step DESC)
  WHERE complete = TRUE;

CREATE TABLE IF NOT EXISTS training_job_event_tokens (
  job_id TEXT PRIMARY KEY REFERENCES training_jobs(id) ON DELETE CASCADE,
  token_sha256 TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  last_generation BIGINT NOT NULL DEFAULT 0,
  last_epoch BIGINT NOT NULL DEFAULT 0,
  last_step BIGINT NOT NULL DEFAULT 0,
  rate_window_started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  rate_count INTEGER NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT training_job_event_tokens_digest_check CHECK (token_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT training_job_event_tokens_progress_check CHECK (
    last_generation BETWEEN 0 AND 1000000000000
    AND last_epoch BETWEEN 0 AND 1000000000000
    AND last_step BETWEEN 0 AND 1000000000000
  ),
  CONSTRAINT training_job_event_tokens_rate_check CHECK (rate_count BETWEEN 0 AND 120)
);
CREATE INDEX IF NOT EXISTS training_job_event_tokens_expiry_idx
  ON training_job_event_tokens(expires_at);

CREATE TABLE IF NOT EXISTS training_job_events (
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  generation BIGINT NOT NULL DEFAULT 0,
  epoch BIGINT NOT NULL DEFAULT 0,
  step BIGINT NOT NULL DEFAULT 0,
  result_json JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (job_id, event_id),
  CONSTRAINT training_job_events_id_check CHECK (event_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
  CONSTRAINT training_job_events_type_check CHECK (event_type IN ('WORKER_GROUP_STARTED', 'CHECKPOINT_COMPLETE', 'TRAINING_PROGRESS')),
  CONSTRAINT training_job_events_progress_check CHECK (
    generation BETWEEN 0 AND 1000000000000
    AND epoch BETWEEN 0 AND 1000000000000
    AND step BETWEEN 0 AND 1000000000000
  ),
  CONSTRAINT training_job_events_result_object_check CHECK (jsonb_typeof(result_json) = 'object')
);
CREATE INDEX IF NOT EXISTS training_job_events_created_idx
  ON training_job_events(job_id, created_at DESC);
