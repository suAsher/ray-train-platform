SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Empty visibility preserves every existing tenant/global catalogue entry.
-- CreatedBy is attribution only and is never converted into an ACL owner.
ALTER TABLE platform_images
  ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN visibility TEXT NOT NULL DEFAULT '',
  ADD COLUMN environment_version_id TEXT NOT NULL DEFAULT '',
  ADD CONSTRAINT platform_images_owner_visibility_check CHECK (
    (visibility = '' AND owner_user_id = '' AND environment_version_id = '') OR
    (visibility IN ('personal', 'team') AND length(trim(owner_user_id)) > 0 AND tenant_id IS NOT NULL AND
      (visibility <> 'personal' OR is_default = FALSE))
  );

CREATE INDEX platform_images_owner_visibility_idx ON platform_images(tenant_id, owner_user_id, visibility);
CREATE UNIQUE INDEX platform_images_environment_version_uidx ON platform_images(environment_version_id)
  WHERE environment_version_id <> '';
