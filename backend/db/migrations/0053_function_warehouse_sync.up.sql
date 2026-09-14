SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Additive control-plane state only. Existing jobs and all schema-52 tables
-- retain their shape and ownership; no backfill or automatic export is run.
CREATE TABLE IF NOT EXISTS function_warehouse_syncs (
 id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL CHECK (owner_id <> ''),
 owner_name TEXT NOT NULL DEFAULT '',
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 job_id TEXT NOT NULL CHECK (job_id <> ''),
 environment TEXT NOT NULL CHECK (environment IN ('production','development')),
 warehouse_id TEXT NOT NULL CHECK (warehouse_id <> ''),
 model_type_id TEXT NOT NULL CHECK (model_type_id <> ''),
 version TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 200),
 paths JSONB NOT NULL CHECK (jsonb_typeof(paths) = 'array'),
 automatic BOOLEAN NOT NULL DEFAULT FALSE,
 state TEXT NOT NULL CHECK (state IN ('WAITING_SOURCE','QUEUED','UPLOADING','REGISTERING','SUCCEEDED','FAILED','WAITING_REAUTH','UNKNOWN','CANCELED')),
 message TEXT NOT NULL DEFAULT '',
 files JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(files) = 'array'),
 run_id TEXT NOT NULL DEFAULT '',
 experiment_id TEXT NOT NULL DEFAULT '',
 target_version_id TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 credential BYTEA,
 lease_id TEXT NOT NULL DEFAULT '',
 lease_expires_at TIMESTAMPTZ,
 UNIQUE(owner_id,tenant_id,idempotency_key),
 CHECK ((lease_id = '' AND lease_expires_at IS NULL) OR (lease_id <> '' AND lease_expires_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS function_warehouse_syncs_job_idx ON function_warehouse_syncs(owner_id,tenant_id,job_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS function_warehouse_syncs_due_idx ON function_warehouse_syncs(next_attempt_at,created_at,id) WHERE state IN ('WAITING_SOURCE','QUEUED') AND lease_id = '';
CREATE INDEX IF NOT EXISTS function_warehouse_syncs_lease_idx ON function_warehouse_syncs(lease_expires_at) WHERE lease_id <> '';
CREATE INDEX IF NOT EXISTS function_warehouse_syncs_owner_active_idx ON function_warehouse_syncs(owner_id,state) WHERE state IN ('WAITING_SOURCE','QUEUED','UPLOADING','REGISTERING','WAITING_REAUTH');

CREATE OR REPLACE FUNCTION function_warehouse_sync_intent_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD) - ARRAY['state','message','files','run_id','experiment_id','target_version_id','updated_at','next_attempt_at','credential','lease_id','lease_expires_at']) IS DISTINCT FROM
    (to_jsonb(NEW) - ARRAY['state','message','files','run_id','experiment_id','target_version_id','updated_at','next_attempt_at','credential','lease_id','lease_expires_at']) THEN
  RAISE EXCEPTION 'warehouse sync source and destination are immutable';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS function_warehouse_sync_intent_immutable ON function_warehouse_syncs;
CREATE TRIGGER function_warehouse_sync_intent_immutable BEFORE UPDATE ON function_warehouse_syncs FOR EACH ROW EXECUTE FUNCTION function_warehouse_sync_intent_immutable();
