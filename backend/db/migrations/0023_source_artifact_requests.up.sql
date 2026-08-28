SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE source_artifacts
  DROP CONSTRAINT IF EXISTS source_artifacts_tenant_user_sha256_key;

CREATE INDEX IF NOT EXISTS source_artifacts_owner_sha256_idx
  ON source_artifacts(tenant_id, user_id, sha256, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS source_artifacts_id_owner_uidx
  ON source_artifacts(id, tenant_id, user_id);

CREATE TABLE IF NOT EXISTS source_artifact_requests (
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  client_request_id TEXT NOT NULL
    CONSTRAINT source_artifact_requests_client_request_id_check
    CHECK (client_request_id ~ '^source-request-[0-9a-f]{24}$'),
  artifact_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (tenant_id, user_id, client_request_id),
  CONSTRAINT source_artifact_requests_user_tenant_fk
    FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE,
  CONSTRAINT source_artifact_requests_artifact_owner_fk
    FOREIGN KEY (artifact_id, tenant_id, user_id) REFERENCES source_artifacts(id, tenant_id, user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS source_artifact_requests_artifact_idx
  ON source_artifact_requests(artifact_id);
