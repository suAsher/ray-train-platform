-- Operator-approved data roots. The claim is pre-provisioned by the
-- infrastructure layer, so no TOS credentials or bucket identifiers are kept
-- in the application database.
CREATE TABLE IF NOT EXISTS storage_assets (
  id TEXT PRIMARY KEY,
  -- NULL tenant + NULL owner is a shared asset. An owner is always scoped to
  -- an explicit tenant and can only be read by that user in that tenant.
  tenant_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
  owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK (kind IN ('dataset', 'checkpoint', 'output')),
  provider TEXT NOT NULL CHECK (provider IN ('tos', 'idc')),
  claim_name TEXT NOT NULL,
  root_prefix TEXT NOT NULL DEFAULT '',
  read_only BOOLEAN NOT NULL,
  browse_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CHECK ((owner_user_id IS NULL) OR (tenant_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS storage_assets_tenant_kind_idx ON storage_assets(tenant_id, kind);
CREATE INDEX IF NOT EXISTS storage_assets_owner_idx ON storage_assets(owner_user_id);
