SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Authorization and historical ownership are deliberately separate. An
-- inactive membership revokes access, while this row keeps old resources
-- referentially intact without granting the identity a role in the tenant.
CREATE TABLE IF NOT EXISTS identity_tenant_ownerships (
  identity_id TEXT NOT NULL REFERENCES local_users(id) ON DELETE RESTRICT,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (identity_id, tenant_id)
);

INSERT INTO identity_tenant_ownerships(identity_id, tenant_id)
SELECT identity.id, legacy.tenant_id
FROM users AS legacy
JOIN local_users AS identity ON identity.id = legacy.id
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

INSERT INTO identity_tenant_ownerships(identity_id, tenant_id)
SELECT identity_id, tenant_id
FROM tenant_memberships
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

INSERT INTO identity_tenant_ownerships(identity_id, tenant_id)
SELECT DISTINCT artifact.user_id, artifact.tenant_id
FROM source_artifacts AS artifact
JOIN local_users AS identity ON identity.id = artifact.user_id
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

INSERT INTO identity_tenant_ownerships(identity_id, tenant_id)
SELECT DISTINCT request.user_id, request.tenant_id
FROM source_artifact_requests AS request
JOIN local_users AS identity ON identity.id = request.user_id
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

INSERT INTO identity_tenant_ownerships(identity_id, tenant_id)
SELECT DISTINCT token.user_id, token.tenant_id
FROM personal_access_tokens AS token
JOIN local_users AS identity ON identity.id = token.user_id
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

INSERT INTO identity_tenant_ownerships(identity_id, tenant_id)
SELECT DISTINCT upload.user_id, upload.tenant_id
FROM data_space_uploads AS upload
JOIN local_users AS identity ON identity.id = upload.user_id
ON CONFLICT (identity_id, tenant_id) DO NOTHING;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM source_artifacts AS resource
    LEFT JOIN identity_tenant_ownerships AS owner
      ON owner.identity_id = resource.user_id AND owner.tenant_id = resource.tenant_id
    WHERE owner.identity_id IS NULL
  ) OR EXISTS (
    SELECT 1 FROM source_artifact_requests AS resource
    LEFT JOIN identity_tenant_ownerships AS owner
      ON owner.identity_id = resource.user_id AND owner.tenant_id = resource.tenant_id
    WHERE owner.identity_id IS NULL
  ) OR EXISTS (
    SELECT 1 FROM personal_access_tokens AS resource
    LEFT JOIN identity_tenant_ownerships AS owner
      ON owner.identity_id = resource.user_id AND owner.tenant_id = resource.tenant_id
    WHERE owner.identity_id IS NULL
  ) OR EXISTS (
    SELECT 1 FROM data_space_uploads AS resource
    LEFT JOIN identity_tenant_ownerships AS owner
      ON owner.identity_id = resource.user_id AND owner.tenant_id = resource.tenant_id
    WHERE owner.identity_id IS NULL
  ) THEN
    RAISE EXCEPTION 'tenant-owned resource has no platform identity';
  END IF;
END;
$$;

ALTER TABLE personal_access_tokens
  DROP CONSTRAINT IF EXISTS personal_access_tokens_user_tenant_fk,
  ADD CONSTRAINT personal_access_tokens_user_tenant_fk
    FOREIGN KEY (user_id, tenant_id)
    REFERENCES identity_tenant_ownerships(identity_id, tenant_id) ON DELETE RESTRICT;

ALTER TABLE source_artifacts
  DROP CONSTRAINT IF EXISTS source_artifacts_user_tenant_fk,
  ADD CONSTRAINT source_artifacts_user_tenant_fk
    FOREIGN KEY (user_id, tenant_id)
    REFERENCES identity_tenant_ownerships(identity_id, tenant_id) ON DELETE RESTRICT;

ALTER TABLE source_artifact_requests
  DROP CONSTRAINT IF EXISTS source_artifact_requests_user_tenant_fk,
  ADD CONSTRAINT source_artifact_requests_user_tenant_fk
    FOREIGN KEY (user_id, tenant_id)
    REFERENCES identity_tenant_ownerships(identity_id, tenant_id) ON DELETE RESTRICT;

ALTER TABLE data_space_uploads
  DROP CONSTRAINT IF EXISTS data_space_uploads_user_id_tenant_id_fkey,
  ADD CONSTRAINT data_space_uploads_user_id_tenant_id_fkey
    FOREIGN KEY (user_id, tenant_id)
    REFERENCES identity_tenant_ownerships(identity_id, tenant_id) ON DELETE RESTRICT;

ALTER TABLE data_mount_bindings
  ADD COLUMN IF NOT EXISTS storage_tenant_id TEXT REFERENCES tenants(id);

-- Retired tenant rows are immutable under the lifecycle fence. They stay NULL
-- and use the legacy tenant_id fallback if ever inspected by an administrator.
UPDATE data_mount_bindings AS binding
SET storage_tenant_id = binding.tenant_id
FROM tenants AS tenant
WHERE binding.scope = 'personal'
  AND binding.storage_tenant_id IS NULL
  AND tenant.id = binding.tenant_id
  AND tenant.retired_at IS NULL;

CREATE INDEX IF NOT EXISTS data_mount_bindings_storage_tenant_idx
  ON data_mount_bindings(storage_tenant_id)
  WHERE scope = 'personal';
