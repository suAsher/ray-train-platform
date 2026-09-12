SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- The reservation row is the durable create intent. Existing training jobs,
-- memberships, quotas and personal storage ownership are not modified.
CREATE TABLE IF NOT EXISTS mlflow_tracking_experiments (
  id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL,
  idempotency_hash TEXT NOT NULL CHECK (idempotency_hash ~ '^[0-9a-f]{64}$'),
  name TEXT NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
  state TEXT NOT NULL CHECK (state IN ('PENDING', 'READY')),
  upstream_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, user_id, idempotency_hash),
  UNIQUE (tenant_id, user_id, name),
  UNIQUE (id, tenant_id, user_id),
  CHECK (state = 'PENDING' OR upstream_id <> '')
);
CREATE UNIQUE INDEX IF NOT EXISTS mlflow_tracking_exp_upstream ON mlflow_tracking_experiments(upstream_id) WHERE upstream_id <> '';
CREATE INDEX IF NOT EXISTS mlflow_tracking_exp_owner ON mlflow_tracking_experiments(tenant_id, user_id, id);

CREATE TABLE IF NOT EXISTS mlflow_tracking_runs (
  id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
  experiment_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  idempotency_hash TEXT NOT NULL CHECK (idempotency_hash ~ '^[0-9a-f]{64}$'),
  name TEXT NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
  state TEXT NOT NULL CHECK (state IN ('PENDING', 'RUNNING', 'FINISHING', 'FINISHED', 'FAILED', 'KILLED')),
  upstream_id TEXT NOT NULL DEFAULT '',
  start_time_ms BIGINT NOT NULL DEFAULT 0,
  end_time_ms BIGINT NOT NULL DEFAULT 0,
  finish_status TEXT NOT NULL DEFAULT '' CHECK (finish_status IN ('', 'FINISHED', 'FAILED', 'KILLED')),
  lease_id TEXT NOT NULL DEFAULT '',
  lease_expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at TIMESTAMPTZ,
  UNIQUE (tenant_id, user_id, idempotency_hash),
  FOREIGN KEY (experiment_id, tenant_id, user_id) REFERENCES mlflow_tracking_experiments(id, tenant_id, user_id) ON DELETE RESTRICT,
  CHECK (state = 'PENDING' OR upstream_id <> ''),
  CHECK ((lease_id = '') = (lease_expires_at IS NULL)),
  CHECK (state NOT IN ('FINISHING', 'FINISHED', 'FAILED', 'KILLED') OR finish_status <> '')
);
CREATE UNIQUE INDEX IF NOT EXISTS mlflow_tracking_run_upstream ON mlflow_tracking_runs(upstream_id) WHERE upstream_id <> '';
CREATE INDEX IF NOT EXISTS mlflow_tracking_run_owner ON mlflow_tracking_runs(tenant_id, user_id, experiment_id, id);
