-- Immutable owner-scoped workspace copies. The backing TOS prefix is derived
-- from tenant_id, user_id and id; it is never persisted as an API-visible
-- location or accepted from a browser request.
CREATE TABLE IF NOT EXISTS workspace_snapshots (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_path TEXT NOT NULL DEFAULT '',
  file_count INTEGER NOT NULL CHECK (file_count > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS workspace_snapshots_owner_created_idx
  ON workspace_snapshots(tenant_id, user_id, created_at DESC, id DESC);
