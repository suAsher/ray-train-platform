-- Platform-owned mount adapters for governed data spaces. No TOS credential
-- is stored here: csi-fsx uses the cluster component's IRSA identity when
-- data-space PVs omit a Secret reference.
CREATE TABLE IF NOT EXISTS data_mount_bindings (
  id TEXT PRIMARY KEY,
  tenant_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
  user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
  scope TEXT NOT NULL CHECK (scope IN ('personal', 'tenant', 'public', 'idc')),
  space_id TEXT NOT NULL,
  claim_name TEXT NOT NULL DEFAULT '',
  service_account_name TEXT NOT NULL DEFAULT '',
  driver TEXT NOT NULL DEFAULT '',
  volume_attributes_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  root_prefix TEXT NOT NULL DEFAULT '',
  read_only BOOLEAN NOT NULL DEFAULT FALSE,
  status TEXT NOT NULL CHECK (status IN ('PENDING', 'READY', 'FAILED')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CHECK (
    (scope = 'personal' AND tenant_id IS NOT NULL AND user_id IS NOT NULL) OR
    (scope IN ('tenant', 'idc') AND tenant_id IS NOT NULL AND user_id IS NULL) OR
    (scope = 'public' AND tenant_id IS NULL AND user_id IS NULL)
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS data_mount_bindings_personal_scope_idx
  ON data_mount_bindings(tenant_id, user_id, scope, space_id)
  WHERE scope = 'personal';
CREATE UNIQUE INDEX IF NOT EXISTS data_mount_bindings_tenant_scope_idx
  ON data_mount_bindings(tenant_id, scope, space_id)
  WHERE scope IN ('tenant', 'idc');
CREATE UNIQUE INDEX IF NOT EXISTS data_mount_bindings_public_scope_idx
  ON data_mount_bindings(scope, space_id)
  WHERE scope = 'public';
