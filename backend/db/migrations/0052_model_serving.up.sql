SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS model_serving_contracts (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
 owner_id TEXT NOT NULL CHECK (owner_id <> ''),
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 active BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_serving_contracts_active_idx ON model_serving_contracts(active,created_at,id);

CREATE TABLE IF NOT EXISTS model_serving_deployments (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
 release_id TEXT NOT NULL REFERENCES model_releases(id),
 model_id TEXT NOT NULL REFERENCES model_catalog(id),
 version_id TEXT NOT NULL REFERENCES model_versions(id),
 contract_id TEXT NOT NULL REFERENCES model_serving_contracts(id),
 owner_id TEXT NOT NULL CHECK (owner_id <> ''),
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 job_id TEXT NOT NULL UNIQUE CHECK (job_id ~ '^job-[0-9a-f]{24}$'),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 job_spec_json JSONB NOT NULL CHECK (jsonb_typeof(job_spec_json) = 'object'),
 state TEXT NOT NULL DEFAULT 'CREATING' CHECK (state IN ('CREATING','SUBMITTED','READY','STOPPING','STOPPED','FAILED','EXPIRED')),
 error TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(owner_id,tenant_id,idempotency_key),
 CHECK (expires_at >= created_at + INTERVAL '1 hour' AND expires_at <= created_at + INTERVAL '7 days'),
 CHECK (COALESCE(snapshot_json #>> '{resources,workerReplicas}', '') = '1' AND COALESCE(snapshot_json #>> '{resources,gpusPerWorker}', '') = '1')
);
CREATE UNIQUE INDEX IF NOT EXISTS model_serving_one_active_idx ON model_serving_deployments(model_id,owner_id) WHERE state IN ('CREATING','SUBMITTED','READY','STOPPING');
CREATE INDEX IF NOT EXISTS model_serving_pending_idx ON model_serving_deployments(updated_at,id) WHERE state IN ('CREATING','SUBMITTED','READY','STOPPING');
CREATE INDEX IF NOT EXISTS model_serving_model_idx ON model_serving_deployments(model_id,id);

CREATE TABLE IF NOT EXISTS model_serving_audits (
 id TEXT PRIMARY KEY,
 deployment_id TEXT NOT NULL REFERENCES model_serving_deployments(id),
 actor_id TEXT NOT NULL,
 action TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_serving_audits_deployment_idx ON model_serving_audits(deployment_id,created_at);

CREATE OR REPLACE FUNCTION model_serving_contracts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD) - ARRAY['active','revision']) IS DISTINCT FROM (to_jsonb(NEW) - ARRAY['active','revision']) THEN
  RAISE EXCEPTION 'serving contract executable snapshot is immutable';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_serving_contracts_immutable ON model_serving_contracts;
CREATE TRIGGER model_serving_contracts_immutable BEFORE UPDATE ON model_serving_contracts FOR EACH ROW EXECUTE FUNCTION model_serving_contracts_immutable();

CREATE OR REPLACE FUNCTION model_serving_deployments_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD) - ARRAY['state','error','revision','updated_at']) IS DISTINCT FROM (to_jsonb(NEW) - ARRAY['state','error','revision','updated_at']) THEN
  RAISE EXCEPTION 'serving provenance and reserved job identity are immutable';
 END IF;
 IF OLD.state IN ('STOPPED','FAILED','EXPIRED') AND to_jsonb(OLD) IS DISTINCT FROM to_jsonb(NEW) THEN
  RAISE EXCEPTION 'terminal serving deployment is immutable';
 END IF;
 IF (OLD.state <> 'CREATING' AND NEW.state = 'CREATING') OR (OLD.state = 'STOPPING' AND NEW.state NOT IN ('STOPPING','STOPPED','FAILED','EXPIRED')) THEN
  RAISE EXCEPTION 'serving deployment state cannot regress';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_serving_deployments_immutable ON model_serving_deployments;
CREATE TRIGGER model_serving_deployments_immutable BEFORE UPDATE ON model_serving_deployments FOR EACH ROW EXECUTE FUNCTION model_serving_deployments_immutable();

CREATE OR REPLACE FUNCTION model_serving_audits_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'serving audit history is append only';
END;
$$;
DROP TRIGGER IF EXISTS model_serving_audits_append_only ON model_serving_audits;
CREATE TRIGGER model_serving_audits_append_only BEFORE UPDATE OR DELETE ON model_serving_audits FOR EACH ROW EXECUTE FUNCTION model_serving_audits_append_only();
