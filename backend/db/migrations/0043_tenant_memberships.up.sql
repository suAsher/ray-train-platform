SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS active_tenant_id TEXT REFERENCES tenants(id);

ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS global_roles JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE local_users
SET active_tenant_id = tenant_id
WHERE active_tenant_id IS NULL;

UPDATE local_users
SET global_roles = '["SuperAdmin"]'::jsonb
WHERE roles ? 'SuperAdmin';

ALTER TABLE local_users
  ALTER COLUMN active_tenant_id SET NOT NULL;

CREATE TABLE IF NOT EXISTS tenant_memberships (
  identity_id TEXT NOT NULL REFERENCES local_users(id) ON DELETE RESTRICT,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  roles JSONB NOT NULL DEFAULT '["Engineer"]'::jsonb,
  status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (identity_id, tenant_id)
);

CREATE INDEX IF NOT EXISTS tenant_memberships_tenant_status_idx
  ON tenant_memberships(tenant_id, status);

INSERT INTO tenant_memberships(identity_id, tenant_id, roles, status, created_at, updated_at)
SELECT id, tenant_id, roles, 'ACTIVE', created_at, updated_at
FROM local_users
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

CREATE TRIGGER tenant_memberships_lifecycle_write_guard
  BEFORE INSERT OR UPDATE OR DELETE ON tenant_memberships
  FOR EACH ROW EXECUTE FUNCTION require_active_tenant_write('tenant_id');
