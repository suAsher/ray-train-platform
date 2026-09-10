SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE local_users
  ADD COLUMN IF NOT EXISTS identity_provider TEXT NOT NULL DEFAULT 'local';

ALTER TABLE local_users
  DROP CONSTRAINT IF EXISTS local_users_identity_provider_check;

ALTER TABLE local_users
  ADD CONSTRAINT local_users_identity_provider_check
  CHECK (identity_provider IN ('local', 'oauth2-proxy'));
