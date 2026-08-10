CREATE UNIQUE INDEX IF NOT EXISTS users_id_tenant_uidx
  ON users(id, tenant_id);

CREATE TABLE IF NOT EXISTS personal_access_tokens (
  id TEXT PRIMARY KEY,
  public_id TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  token_digest TEXT NOT NULL,
  scopes JSONB NOT NULL DEFAULT '[]'::jsonb
    CONSTRAINT personal_access_tokens_scopes_array_check CHECK (jsonb_typeof(scopes) = 'array'),
  expires_at TIMESTAMPTZ NOT NULL,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT personal_access_tokens_user_tenant_fk
    FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS personal_access_tokens_owner_idx
  ON personal_access_tokens(tenant_id, user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS personal_access_tokens_active_expiry_idx
  ON personal_access_tokens(expires_at)
  WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS source_artifacts (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  sha256 TEXT NOT NULL
    CONSTRAINT source_artifacts_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes BIGINT NOT NULL
    CONSTRAINT source_artifacts_size_check CHECK (size_bytes BETWEEN 1 AND 2147483648),
  object_key TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'PENDING'
    CONSTRAINT source_artifacts_state_check CHECK (state IN ('PENDING', 'READY', 'FAILED', 'EXPIRED')),
  upload_expires_at TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ,
  last_referenced_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT source_artifacts_user_tenant_fk
    FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE,
  CONSTRAINT source_artifacts_tenant_user_sha256_key
    UNIQUE (tenant_id, user_id, sha256)
);
CREATE INDEX IF NOT EXISTS source_artifacts_owner_created_idx
  ON source_artifacts(tenant_id, user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS source_artifacts_state_expiry_idx
  ON source_artifacts(state, upload_expires_at);

ALTER TABLE training_jobs
  ADD COLUMN IF NOT EXISTS source_artifact_id TEXT REFERENCES source_artifacts(id) ON DELETE SET NULL;
ALTER TABLE training_jobs
  ADD COLUMN IF NOT EXISTS submission_origin TEXT NOT NULL DEFAULT 'portal';
ALTER TABLE training_jobs
  ADD COLUMN IF NOT EXISTS external_submission_id TEXT;

CREATE INDEX IF NOT EXISTS training_jobs_source_artifact_idx
  ON training_jobs(source_artifact_id)
  WHERE source_artifact_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS training_jobs_submission_origin_created_idx
  ON training_jobs(tenant_id, submission_origin, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS training_jobs_tenant_external_submission_uidx
  ON training_jobs(tenant_id, external_submission_id)
  WHERE external_submission_id IS NOT NULL AND external_submission_id <> '';
