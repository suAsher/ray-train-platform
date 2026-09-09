SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS idc_sync_connectors (
  id TEXT PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$'),
  name TEXT NOT NULL CHECK (btrim(name) <> ''),
  source_space TEXT NOT NULL CHECK (source_space = 'idc-original'),
  source_relative_path TEXT NOT NULL CHECK (source_relative_path <> '' AND source_relative_path !~ '(^/|(^|/)\\.\\.?($|/))'),
  mirror_prefix TEXT NOT NULL CHECK (mirror_prefix <> '' AND mirror_prefix !~ '(^/|(^|/)\\.\\.?($|/))'),
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_by TEXT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS idc_sync_runs (
  id TEXT PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$'),
  connector_id TEXT NOT NULL REFERENCES idc_sync_connectors(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$'),
  mode TEXT NOT NULL CHECK (mode IN ('PLAN', 'SYNC')),
  state TEXT NOT NULL CHECK (state IN ('PENDING', 'PLANNING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED')),
  requested_by TEXT NOT NULL REFERENCES users(id),
  inventory_sha256 TEXT NOT NULL DEFAULT '' CHECK (inventory_sha256 = '' OR inventory_sha256 ~ '^[0-9a-f]{64}$'),
  inventory_object_key TEXT NOT NULL DEFAULT '',
  source_object_count BIGINT NOT NULL DEFAULT 0 CHECK (source_object_count >= 0),
  source_bytes BIGINT NOT NULL DEFAULT 0 CHECK (source_bytes >= 0),
  new_object_count BIGINT NOT NULL DEFAULT 0 CHECK (new_object_count >= 0),
  changed_object_count BIGINT NOT NULL DEFAULT 0 CHECK (changed_object_count >= 0),
  reused_object_count BIGINT NOT NULL DEFAULT 0 CHECK (reused_object_count >= 0),
  failure_reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  CHECK ((state = 'SUCCEEDED' AND inventory_sha256 <> '' AND finished_at IS NOT NULL)
      OR (state IN ('FAILED', 'CANCELLED') AND finished_at IS NOT NULL)
      OR (state IN ('PENDING', 'PLANNING', 'RUNNING') AND finished_at IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idc_sync_runs_connector_idempotency_idx ON idc_sync_runs(connector_id, idempotency_key);
CREATE INDEX IF NOT EXISTS idc_sync_runs_connector_created_idx ON idc_sync_runs(connector_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idc_sync_runs_active_idx ON idc_sync_runs(connector_id, created_at) WHERE state IN ('PENDING', 'PLANNING', 'RUNNING');

CREATE TABLE IF NOT EXISTS idc_sync_inventory_entries (
  run_id TEXT NOT NULL REFERENCES idc_sync_runs(id) ON DELETE CASCADE,
  relative_path TEXT NOT NULL CHECK (relative_path <> '' AND relative_path !~ '(^/|(^|/)\\.\\.?($|/))'),
  size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
  modified_at TIMESTAMPTZ NOT NULL,
  sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  object_key TEXT NOT NULL CHECK (object_key ~ '/idc-raw/sha256/[0-9a-f]{2}/[0-9a-f]{64}$'),
  PRIMARY KEY (run_id, relative_path)
);
CREATE INDEX IF NOT EXISTS idc_sync_inventory_entries_sha_idx ON idc_sync_inventory_entries(sha256);

-- This table is the cleanup fence for immutable raw blobs. A later cleanup
-- worker may delete an object only after its reference count reaches zero;
-- source-side deletion never maps directly to a TOS delete.
CREATE TABLE IF NOT EXISTS idc_sync_object_refs (
  sha256 TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  object_key TEXT NOT NULL UNIQUE CHECK (object_key ~ '/idc-raw/sha256/[0-9a-f]{2}/[0-9a-f]{64}$'),
  reference_count BIGINT NOT NULL CHECK (reference_count > 0),
  size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE dataset_versions ADD COLUMN IF NOT EXISTS source_sync_run_id TEXT REFERENCES idc_sync_runs(id) ON DELETE RESTRICT;
ALTER TABLE dataset_versions ADD COLUMN IF NOT EXISTS source_inventory_sha256 TEXT NOT NULL DEFAULT '' CHECK (source_inventory_sha256 = '' OR source_inventory_sha256 ~ '^[0-9a-f]{64}$');
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS source_sync_run_id TEXT REFERENCES idc_sync_runs(id) ON DELETE RESTRICT;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS source_inventory_sha256 TEXT NOT NULL DEFAULT '' CHECK (source_inventory_sha256 = '' OR source_inventory_sha256 ~ '^[0-9a-f]{64}$');
