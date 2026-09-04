SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS data_space_uploads (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  space_id TEXT NOT NULL,
  root_prefix TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  content_type TEXT NOT NULL,
  size_bytes BIGINT NOT NULL CHECK (size_bytes > 268435456),
  part_size_bytes BIGINT NOT NULL CHECK (part_size_bytes BETWEEN 1 AND 5368709120),
  total_parts INTEGER NOT NULL CHECK (total_parts BETWEEN 1 AND 10000),
  provider_upload_id TEXT NOT NULL CHECK (provider_upload_id <> ''),
  state TEXT NOT NULL CHECK (state IN ('ACTIVE', 'COMPLETING', 'COMPLETED', 'ABORTING', 'ABORTED')),
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS data_space_uploads_active_target_uidx
  ON data_space_uploads(tenant_id, user_id, space_id, relative_path)
  WHERE state IN ('ACTIVE', 'COMPLETING', 'ABORTING');

CREATE INDEX IF NOT EXISTS data_space_uploads_expiry_idx
  ON data_space_uploads(expires_at)
  WHERE state = 'ACTIVE';

CREATE TABLE IF NOT EXISTS data_space_upload_parts (
  session_id TEXT NOT NULL REFERENCES data_space_uploads(id) ON DELETE CASCADE,
  part_number INTEGER NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
  size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 5368709120),
  sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  etag TEXT NOT NULL CHECK (etag <> ''),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (session_id, part_number)
);
