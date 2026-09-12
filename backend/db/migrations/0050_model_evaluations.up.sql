SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS model_evaluators (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
 owner_id TEXT NOT NULL CHECK (owner_id <> ''),
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 active BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_evaluators_active_idx ON model_evaluators(active, created_at, id);

CREATE TABLE IF NOT EXISTS model_evaluations (
 id TEXT PRIMARY KEY,
 model_id TEXT NOT NULL REFERENCES model_catalog(id),
 version_id TEXT NOT NULL REFERENCES model_versions(id),
 dataset_id TEXT NOT NULL REFERENCES datasets(id),
 dataset_version_id TEXT NOT NULL REFERENCES dataset_versions(id),
 dataset_visibility TEXT NOT NULL CHECK (dataset_visibility IN ('PUBLIC','TEAM')),
 dataset_tenant_id TEXT NOT NULL DEFAULT '',
 evaluator_id TEXT NOT NULL REFERENCES model_evaluators(id),
 owner_id TEXT NOT NULL CHECK (owner_id <> ''),
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 -- The ID is reserved before the training job exists. It is unique and
 -- immutable, but deliberately has no premature training_jobs foreign key.
 job_id TEXT NOT NULL UNIQUE CHECK (job_id <> ''),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 job_spec_json JSONB NOT NULL CHECK (jsonb_typeof(job_spec_json) = 'object'),
 state TEXT NOT NULL DEFAULT 'CREATING' CHECK (state IN ('CREATING','SUBMITTED','RUNNING','SUCCEEDED','FAILED','CANCELLED')),
 report_state TEXT NOT NULL DEFAULT 'PENDING' CHECK (report_state IN ('PENDING','VALID','INVALID','MISSING')),
 report_json JSONB NOT NULL DEFAULT 'null'::jsonb CHECK (jsonb_typeof(report_json) IN ('object','null')),
 report_sha256 TEXT NOT NULL DEFAULT '' CHECK (report_sha256 = '' OR report_sha256 ~ '^[0-9a-f]{64}$'),
 error TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 finished_at TIMESTAMPTZ,
 UNIQUE (owner_id, tenant_id, idempotency_key),
 CHECK ((dataset_visibility = 'PUBLIC' AND dataset_tenant_id = '') OR (dataset_visibility = 'TEAM' AND dataset_tenant_id <> '')),
 CHECK (report_state <> 'VALID' OR (report_sha256 <> '' AND jsonb_typeof(report_json) = 'object')),
 CHECK (state <> 'SUCCEEDED' OR report_state = 'VALID'),
 CHECK ((state IN ('SUCCEEDED','FAILED','CANCELLED')) = (finished_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS model_evaluations_version_idx ON model_evaluations(model_id, version_id, id);
CREATE INDEX IF NOT EXISTS model_evaluations_visibility_idx ON model_evaluations(dataset_visibility, dataset_tenant_id, id);
CREATE INDEX IF NOT EXISTS model_evaluations_active_owner_idx ON model_evaluations(owner_id, state) WHERE state IN ('CREATING','SUBMITTED','RUNNING');

CREATE TABLE IF NOT EXISTS model_evaluation_audits (
 id TEXT PRIMARY KEY,
 evaluation_id TEXT NOT NULL REFERENCES model_evaluations(id),
 actor_id TEXT NOT NULL,
 action TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_evaluation_audits_evaluation_idx ON model_evaluation_audits(evaluation_id, created_at);

CREATE OR REPLACE FUNCTION model_evaluators_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD) - ARRAY['active','revision']) IS DISTINCT FROM (to_jsonb(NEW) - ARRAY['active','revision']) THEN
  RAISE EXCEPTION 'evaluator executable snapshot is immutable';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_evaluators_immutable ON model_evaluators;
CREATE TRIGGER model_evaluators_immutable BEFORE UPDATE ON model_evaluators FOR EACH ROW EXECUTE FUNCTION model_evaluators_immutable();

CREATE OR REPLACE FUNCTION model_evaluations_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD) - ARRAY['state','report_state','report_json','report_sha256','error','revision','updated_at','finished_at'])
    IS DISTINCT FROM (to_jsonb(NEW) - ARRAY['state','report_state','report_json','report_sha256','error','revision','updated_at','finished_at']) THEN
  RAISE EXCEPTION 'evaluation provenance and reserved job identity are immutable';
 END IF;
 IF OLD.report_sha256 <> '' AND ROW(NEW.report_json,NEW.report_sha256) IS DISTINCT FROM ROW(OLD.report_json,OLD.report_sha256) THEN
  RAISE EXCEPTION 'accepted evaluation report is immutable';
 END IF;
 IF OLD.state IN ('SUCCEEDED','FAILED','CANCELLED') AND to_jsonb(OLD) IS DISTINCT FROM to_jsonb(NEW) THEN
  RAISE EXCEPTION 'terminal evaluation is immutable';
 END IF;
 IF (OLD.state = 'SUBMITTED' AND NEW.state = 'CREATING') OR (OLD.state = 'RUNNING' AND NEW.state IN ('CREATING','SUBMITTED')) THEN
  RAISE EXCEPTION 'evaluation state cannot regress';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_evaluations_immutable ON model_evaluations;
CREATE TRIGGER model_evaluations_immutable BEFORE UPDATE ON model_evaluations FOR EACH ROW EXECUTE FUNCTION model_evaluations_immutable();
