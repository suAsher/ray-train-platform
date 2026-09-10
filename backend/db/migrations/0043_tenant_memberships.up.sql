SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS active_tenant_id TEXT REFERENCES tenants(id);

ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS global_roles JSONB NOT NULL DEFAULT '[]'::jsonb;

-- Retired identities are intentionally immutable under the lifecycle trigger.
-- They cannot authenticate, so leave their new compatibility columns at the
-- safe defaults instead of weakening the retirement fence for a backfill.
UPDATE local_users AS identity
SET active_tenant_id = identity.tenant_id
FROM tenants AS tenant
WHERE identity.active_tenant_id IS NULL
  AND tenant.id = identity.tenant_id
  AND tenant.retired_at IS NULL;

UPDATE local_users AS identity
SET global_roles = '["SuperAdmin"]'::jsonb
FROM tenants AS tenant
WHERE identity.roles ? 'SuperAdmin'
  AND tenant.id = identity.tenant_id
  AND tenant.retired_at IS NULL;

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
SELECT identity.id, identity.tenant_id, identity.roles, 'ACTIVE', identity.created_at, identity.updated_at
FROM local_users AS identity
JOIN tenants AS tenant ON tenant.id = identity.tenant_id
WHERE identity.decommissioned_at IS NULL
  AND tenant.retired_at IS NULL
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

CREATE TRIGGER tenant_memberships_lifecycle_write_guard
  BEFORE INSERT OR UPDATE OR DELETE ON tenant_memberships
  FOR EACH ROW EXECUTE FUNCTION require_active_tenant_write('tenant_id');
