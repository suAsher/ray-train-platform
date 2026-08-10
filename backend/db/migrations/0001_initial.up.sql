CREATE TABLE IF NOT EXISTS tenants (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  namespace TEXT NOT NULL UNIQUE,
  local_queue TEXT NOT NULL,
  gpu_quota_limit INTEGER NOT NULL DEFAULT 0,
  cpu_quota_millis BIGINT NOT NULL DEFAULT 0,
  memory_quota_bytes BIGINT NOT NULL DEFAULT 0,
  max_priority TEXT NOT NULL DEFAULT 'normal',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  oidc_subject TEXT NOT NULL UNIQUE,
  username TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  roles JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS training_jobs (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  user_id TEXT NOT NULL REFERENCES users(id),
  name TEXT NOT NULL,
  spec_json JSONB NOT NULL,
  desired_state TEXT NOT NULL DEFAULT 'ACTIVE',
  observed_state TEXT NOT NULL DEFAULT 'SUBMITTED',
  status_reason TEXT NOT NULL DEFAULT '',
  status_message TEXT NOT NULL DEFAULT '',
  kubernetes_ns TEXT NOT NULL,
  ray_job_name TEXT NOT NULL DEFAULT '',
  ray_job_uid TEXT NOT NULL DEFAULT '',
  ray_cluster_name TEXT NOT NULL DEFAULT '',
  resource_version TEXT NOT NULL DEFAULT '',
  retry_count INTEGER NOT NULL DEFAULT 0,
  timeout_seconds BIGINT NOT NULL DEFAULT 0,
  cleanup_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_observed_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS training_jobs_tenant_status_idx ON training_jobs(tenant_id, observed_state);

CREATE TABLE IF NOT EXISTS job_events (
  id BIGSERIAL PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  component TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS job_events_job_created_idx ON job_events(job_id, created_at DESC);

CREATE TABLE IF NOT EXISTS job_artifacts (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES training_jobs(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  kind TEXT NOT NULL,
  uri TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS dev_workspaces (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  user_id TEXT NOT NULL REFERENCES users(id),
  name TEXT NOT NULL,
  namespace TEXT NOT NULL,
  raycluster_name TEXT NOT NULL,
  jupyter_service TEXT NOT NULL DEFAULT '',
  observed_state TEXT NOT NULL DEFAULT 'SUBMITTED',
  gpu_count INTEGER NOT NULL DEFAULT 1,
  idle_ttl_seconds BIGINT NOT NULL DEFAULT 3600,
  expires_at TIMESTAMPTZ,
  snapshot_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, user_id)
);

CREATE TABLE IF NOT EXISTS audit_logs (
  id BIGSERIAL PRIMARY KEY,
  tenant_id TEXT,
  user_id TEXT,
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  key TEXT NOT NULL,
  response_json JSONB NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (tenant_id, key)
);

CREATE TABLE IF NOT EXISTS outbox_events (
  id TEXT PRIMARY KEY,
  aggregate_type TEXT NOT NULL,
  aggregate_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT 'PENDING',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  locked_at TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox_events(status, next_attempt_at);
