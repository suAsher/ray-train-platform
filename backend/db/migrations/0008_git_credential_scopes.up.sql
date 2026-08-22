-- Git credentials started as one tenant-wide token per host. Personal private
-- repositories require user isolation, so preserve existing entries as team
-- credentials and make a personal credential distinct by owner.
ALTER TABLE git_credentials ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'team';
ALTER TABLE git_credentials ADD COLUMN IF NOT EXISTS owner_user_id TEXT NOT NULL DEFAULT '';

ALTER TABLE git_credentials DROP CONSTRAINT IF EXISTS git_credentials_scope_check;
ALTER TABLE git_credentials
  ADD CONSTRAINT git_credentials_scope_check CHECK (scope IN ('personal', 'team'));

DROP INDEX IF EXISTS git_credentials_tenant_host_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS git_credentials_tenant_scope_owner_host_uidx
  ON git_credentials(tenant_id, scope, owner_user_id, host);
