-- Dashboard access tickets store only a SHA-256 digest. OIDC subjects are not
-- required to have a corresponding local users row, so these identity fields
-- deliberately remain unconstrained.
CREATE TABLE IF NOT EXISTS mlflow_dashboard_tickets (
  token_hash TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_mlflow_dashboard_tickets_expiry ON mlflow_dashboard_tickets(expires_at);
