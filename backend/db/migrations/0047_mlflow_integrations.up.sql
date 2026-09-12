SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Integration identities never occupy local_users or inherit personal storage.
CREATE TABLE mlflow_integrations (
 id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
 tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
 owner_user_id TEXT NOT NULL REFERENCES local_users(id) ON DELETE RESTRICT,
 name TEXT NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
 allow_create_experiments BOOLEAN NOT NULL DEFAULT FALSE,
 revoked_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX mlflow_integrations_owner ON mlflow_integrations(tenant_id,owner_user_id,id);
CREATE TABLE mlflow_integration_tokens (
 id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
 public_id TEXT NOT NULL UNIQUE,
 integration_id TEXT NOT NULL REFERENCES mlflow_integrations(id) ON DELETE RESTRICT,
 token_digest TEXT NOT NULL CHECK (token_digest ~ '^[0-9a-f]{64}$'),
 scopes JSONB NOT NULL CHECK (jsonb_typeof(scopes) = 'array'),
 expires_at TIMESTAMPTZ NOT NULL,
 last_used_at TIMESTAMPTZ,
 revoked_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK(expires_at > created_at AND expires_at <= created_at + INTERVAL '30 days')
);
CREATE INDEX mlflow_integration_tokens_identity ON mlflow_integration_tokens(integration_id,id);
CREATE TABLE mlflow_integration_grants (
 integration_id TEXT NOT NULL REFERENCES mlflow_integrations(id) ON DELETE RESTRICT,
 experiment_id TEXT NOT NULL REFERENCES mlflow_tracking_experiments(id) ON DELETE RESTRICT,
 permissions JSONB NOT NULL CHECK (jsonb_typeof(permissions) = 'array'),
 revoked_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (integration_id,experiment_id)
);
-- Revoked rows remain as tombstones: retrying experiment creation cannot revive a grant.
CREATE TABLE mlflow_integration_audit (
 id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
 tenant_id TEXT NOT NULL,
 actor_id TEXT NOT NULL,
 integration_id TEXT NOT NULL,
 action TEXT NOT NULL,
 resource_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX mlflow_integration_audit_owner ON mlflow_integration_audit(tenant_id,actor_id,created_at);
