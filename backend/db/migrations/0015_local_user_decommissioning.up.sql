ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS decommissioned_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS local_users_active_tenant_username_idx
  ON local_users (tenant_id, username)
  WHERE decommissioned_at IS NULL;
