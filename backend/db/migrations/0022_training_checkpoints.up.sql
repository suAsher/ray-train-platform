-- Managed event ingestion is on the training hot path. Bound lock waits so a
-- deployment rolls back instead of stalling every backend replica.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS training_checkpoints (
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
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
  PRIMARY KEY (job_id, id),
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
  generation BIGINT NOT NULL DEFAULT 1,
  epoch BIGINT NOT NULL DEFAULT 0,
  step BIGINT NOT NULL DEFAULT 0,
  result_json JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (job_id, event_id),
  CONSTRAINT training_job_events_id_check CHECK (event_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
  CONSTRAINT training_job_events_type_check CHECK (event_type IN ('WORKER_GROUP_STARTED', 'CHECKPOINT_COMPLETE', 'TRAINING_PROGRESS')),
  CONSTRAINT training_job_events_progress_check CHECK (
    generation BETWEEN 1 AND 1000000000000
    AND epoch BETWEEN 0 AND 1000000000000
    AND step BETWEEN 0 AND 1000000000000
  ),
  CONSTRAINT training_job_events_result_object_check CHECK (jsonb_typeof(result_json) = 'object')
);
CREATE INDEX IF NOT EXISTS training_job_events_created_idx
  ON training_job_events(job_id, created_at DESC);

-- Kubernetes creation and foreground deletion cross a database/API boundary.
-- Keep one durable row per managed cluster attempt so a backend crash cannot
-- lose an unadopted resource or a pending cleanup intent.
CREATE TABLE IF NOT EXISTS managed_attempt_resources (
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  cluster_attempt INTEGER NOT NULL,
  namespace TEXT NOT NULL,
  ray_job_name TEXT NOT NULL,
  ray_job_uid TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'RESERVED',
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_version BIGINT NOT NULL DEFAULT 0,
  resource_fence BIGINT NOT NULL DEFAULT 0,
  lease_expires_at TIMESTAMPTZ,
  cleanup_failures INTEGER NOT NULL DEFAULT 0,
  cleanup_last_error TEXT NOT NULL DEFAULT '',
  next_check_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (job_id, cluster_attempt),
  UNIQUE (namespace, ray_job_name),
  CONSTRAINT managed_attempt_resources_attempt_check CHECK (cluster_attempt BETWEEN 1 AND 1000000),
  CONSTRAINT managed_attempt_resources_namespace_check CHECK (
    length(namespace) BETWEEN 1 AND 63
    AND namespace ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'
  ),
  CONSTRAINT managed_attempt_resources_name_check CHECK (
    length(ray_job_name) BETWEEN 1 AND 63
    AND ray_job_name ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'
  ),
  CONSTRAINT managed_attempt_resources_uid_check CHECK (length(ray_job_uid) <= 253),
  CONSTRAINT managed_attempt_resources_state_check CHECK (state IN ('RESERVED', 'CREATING', 'ACTIVATING', 'ACTIVE', 'RETIRING', 'CLEANED', 'QUARANTINED')),
  CONSTRAINT managed_attempt_resources_lease_check CHECK (
    lease_version >= 0
    AND resource_fence >= 0
    AND length(lease_owner) <= 128
    AND (
      (state = 'CREATING' AND lease_owner <> '' AND lease_expires_at IS NOT NULL)
      OR (state <> 'CREATING' AND lease_owner = '' AND lease_expires_at IS NULL)
    )
  ),
  CONSTRAINT managed_attempt_resources_cleanup_check CHECK (
    cleanup_failures >= 0
    AND length(cleanup_last_error) <= 4096
  )
);
CREATE INDEX IF NOT EXISTS managed_attempt_resources_cleanup_idx
  ON managed_attempt_resources(state, next_check_at, updated_at, job_id, cluster_attempt)
  WHERE state IN ('RETIRING', 'RESERVED', 'CREATING');
CREATE INDEX IF NOT EXISTS managed_attempt_resources_job_state_idx
  ON managed_attempt_resources(job_id, state, cluster_attempt);
CREATE INDEX IF NOT EXISTS managed_attempt_resources_tombstone_probe_idx
  ON managed_attempt_resources(next_check_at, job_id, cluster_attempt)
  WHERE state = 'CLEANED';

-- 0022 has not shipped yet, but backfilling keeps development databases and
-- rolling test environments coherent when the migration is reapplied there.
INSERT INTO managed_attempt_resources (
  job_id, cluster_attempt, namespace, ray_job_name, ray_job_uid, state,
  lease_owner, lease_version, lease_expires_at, created_at, updated_at
)
SELECT
  id, cluster_attempt, COALESCE(NULLIF(kubernetes_ns, ''), 'tenant-' || tenant_id), ray_job_name, ray_job_uid,
  CASE
    WHEN desired_state = 'CANCELED' OR observed_state IN ('SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT') THEN 'RETIRING'
    WHEN ray_job_uid <> '' THEN 'ACTIVE'
    ELSE 'RESERVED'
  END,
  '', 0, NULL, created_at, updated_at
FROM training_jobs
WHERE training_engine = 'ray-train' AND ray_job_name <> ''
ON CONFLICT (job_id, cluster_attempt) DO NOTHING;
