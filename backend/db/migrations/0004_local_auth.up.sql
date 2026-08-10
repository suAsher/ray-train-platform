-- Local username/password accounts. These exist so the platform can be
-- operated and tested without an external identity provider; Keycloak/OIDC
-- remains the production path and the two can run side by side.
CREATE TABLE IF NOT EXISTS local_users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL DEFAULT '',
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  roles JSONB NOT NULL DEFAULT '[]'::jsonb,
  password_hash TEXT NOT NULL,
  disabled BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS local_users_tenant_idx ON local_users(tenant_id);

-- Only the HMAC digest of a session token is stored, matching the personal
-- access token design, so the table is not a credential store.
CREATE TABLE IF NOT EXISTS local_sessions (
  id TEXT PRIMARY KEY,
  public_id TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL REFERENCES local_users(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  token_digest TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS local_sessions_user_idx ON local_sessions(user_id);
CREATE INDEX IF NOT EXISTS local_sessions_expiry_idx ON local_sessions(expires_at);
