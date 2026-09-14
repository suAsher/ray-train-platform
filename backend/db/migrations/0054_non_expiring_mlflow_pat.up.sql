SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE personal_access_tokens
  ALTER COLUMN expires_at DROP NOT NULL;

ALTER TABLE personal_access_tokens
  ADD CONSTRAINT personal_access_tokens_non_expiring_mlflow_check
  CHECK (
    expires_at IS NOT NULL
    OR (scopes = '["mlflow:full"]'::jsonb AND btrim(user_id) NOT LIKE 'integration:%')
  );
