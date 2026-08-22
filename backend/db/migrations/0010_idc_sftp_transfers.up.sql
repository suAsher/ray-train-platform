-- Personal IDC SFTP connections contain only public metadata and a Kubernetes
-- Secret name. The generated private key is never stored in PostgreSQL.
CREATE TABLE IF NOT EXISTS idc_sftp_connections (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  remote_username TEXT NOT NULL,
  public_key TEXT NOT NULL,
  secret_name TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending', 'ready', 'failed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, user_id)
);

CREATE INDEX IF NOT EXISTS idc_sftp_connections_owner_state_idx
  ON idc_sftp_connections(tenant_id, user_id, state);

-- Copy requests reference logical user-owned locations only. They contain no
-- remote host, cloud credential, private key, bucket, or raw TOS prefix.
CREATE TABLE IF NOT EXISTS data_transfers (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  direction TEXT NOT NULL CHECK (direction IN ('idc-to-tos', 'tos-to-idc')),
  idc_relative_path TEXT NOT NULL,
  tos_space TEXT NOT NULL CHECK (tos_space = 'my-files'),
  tos_relative_path TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS data_transfers_owner_created_idx
  ON data_transfers(tenant_id, user_id, created_at DESC, id DESC);
