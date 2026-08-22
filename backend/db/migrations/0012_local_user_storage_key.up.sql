-- Local account storage roots are immutable after provisioning. Existing
-- accounts remain on their opaque subject roots until the administrator runs
-- the separately audited data-root migration.
ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS storage_key TEXT NOT NULL DEFAULT '';

UPDATE local_users
  SET storage_key = id
  WHERE storage_key = '';
