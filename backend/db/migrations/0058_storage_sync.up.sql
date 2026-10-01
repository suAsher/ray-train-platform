SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE storage_sync_plans (
 id TEXT PRIMARY KEY CHECK (id <> ''),
 revision BIGINT NOT NULL CHECK (revision >= 1),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE storage_sync_plan_revisions (
 plan_id TEXT NOT NULL REFERENCES storage_sync_plans(id),
 revision BIGINT NOT NULL CHECK (revision >= 1),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (plan_id, revision)
);
CREATE TABLE storage_sync_previews (
 id TEXT PRIMARY KEY CHECK (id <> ''),
 plan_id TEXT NOT NULL DEFAULT '',
 actor TEXT NOT NULL,
 state TEXT NOT NULL,
 attempt INTEGER NOT NULL CHECK (attempt >= 0),
 generation BIGINT NOT NULL CHECK (generation >= 0),
 sequence BIGINT NOT NULL CHECK (sequence >= 0),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 created_at TIMESTAMPTZ NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX storage_sync_previews_state_idx ON storage_sync_previews(state, created_at, id);

CREATE TABLE storage_sync_runs (
 id TEXT PRIMARY KEY CHECK (id <> ''),
 plan_id TEXT NOT NULL,
 config_revision BIGINT NOT NULL,
 requested_by TEXT NOT NULL,
 idempotency_key TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK (state IN ('QUEUED','RUNNING','PAUSING','PAUSED','CANCELLING','SUCCEEDED','FAILED','CANCELLED')),
 attempt INTEGER NOT NULL CHECK (attempt >= 1),
 generation BIGINT NOT NULL CHECK (generation >= 1),
 sequence BIGINT NOT NULL CHECK (sequence >= 0),
 job_uid TEXT NOT NULL DEFAULT '',
 identity_json JSONB NOT NULL CHECK (jsonb_typeof(identity_json) = 'object'),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 scheduled_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL,
 finished_at TIMESTAMPTZ,
 FOREIGN KEY (plan_id, config_revision) REFERENCES storage_sync_plan_revisions(plan_id, revision),
 CHECK (finished_at IS NULL OR state IN ('SUCCEEDED','FAILED','CANCELLED'))
);
-- PAUSED and a failed worker awaiting proof of shutdown still occupy the plan.
CREATE UNIQUE INDEX storage_sync_runs_active_plan_idx ON storage_sync_runs(plan_id) WHERE finished_at IS NULL;
CREATE UNIQUE INDEX storage_sync_runs_request_idx ON storage_sync_runs(plan_id, requested_by, idempotency_key) WHERE idempotency_key <> '';
CREATE UNIQUE INDEX storage_sync_runs_schedule_idx ON storage_sync_runs(plan_id, config_revision, scheduled_at) WHERE scheduled_at IS NOT NULL;
CREATE INDEX storage_sync_runs_plan_idx ON storage_sync_runs(plan_id, created_at, id);

CREATE TABLE storage_sync_attempts (
 run_id TEXT NOT NULL REFERENCES storage_sync_runs(id),
 attempt INTEGER NOT NULL CHECK (attempt >= 1),
 generation BIGINT NOT NULL CHECK (generation >= 1),
 snapshot_json JSONB NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (run_id, attempt)
);
CREATE TABLE storage_sync_path_locks (
 run_id TEXT NOT NULL REFERENCES storage_sync_runs(id),
 attempt INTEGER NOT NULL CHECK (attempt >= 1),
 storage_key TEXT NOT NULL,
 prefix TEXT NOT NULL,
 mode TEXT NOT NULL CHECK (mode IN ('READ','WRITE')),
 PRIMARY KEY (run_id, storage_key, prefix, mode),
 FOREIGN KEY (run_id, attempt) REFERENCES storage_sync_attempts(run_id, attempt)
);
CREATE INDEX storage_sync_path_locks_root_idx ON storage_sync_path_locks(storage_key, prefix);

CREATE TABLE storage_sync_files (
 run_id TEXT NOT NULL REFERENCES storage_sync_runs(id),
 mapping_index INTEGER NOT NULL CHECK (mapping_index >= 0),
 path_key TEXT NOT NULL CHECK (path_key ~ '^[0-9a-f]{64}$'),
 relative_path TEXT NOT NULL CHECK (relative_path <> ''),
 attempt INTEGER NOT NULL CHECK (attempt >= 1),
 generation BIGINT NOT NULL CHECK (generation >= 1),
 state TEXT NOT NULL CHECK (state IN ('VERIFIED','REUSED','FAILED')),
 size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
 error_code TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (run_id, mapping_index, path_key),
 FOREIGN KEY (run_id, attempt) REFERENCES storage_sync_attempts(run_id, attempt)
);

CREATE FUNCTION storage_sync_revisions_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'storage sync plan revisions are append only';
END;
$$;
CREATE TRIGGER storage_sync_revisions_immutable BEFORE UPDATE OR DELETE ON storage_sync_plan_revisions FOR EACH ROW EXECUTE FUNCTION storage_sync_revisions_immutable();

CREATE FUNCTION storage_sync_run_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.plan_id, NEW.config_revision, NEW.requested_by, NEW.idempotency_key, NEW.identity_json, NEW.scheduled_at, NEW.created_at)
  IS DISTINCT FROM ROW(OLD.plan_id, OLD.config_revision, OLD.requested_by, OLD.idempotency_key, OLD.identity_json, OLD.scheduled_at, OLD.created_at) THEN
  RAISE EXCEPTION 'storage sync run identity is immutable';
 END IF;
 IF OLD.finished_at IS NOT NULL AND to_jsonb(OLD) IS DISTINCT FROM to_jsonb(NEW)
  AND NOT (OLD.state = 'FAILED' AND OLD.snapshot_json->'private'->>'stopVerified' = 'true'
   AND NEW.state = 'QUEUED' AND NEW.finished_at IS NULL
   AND NEW.attempt = OLD.attempt + 1 AND NEW.generation = OLD.generation + 1) THEN
  RAISE EXCEPTION 'finished storage sync run is immutable';
 END IF;
 IF NEW.attempt < OLD.attempt OR NEW.generation < OLD.generation
  OR (NEW.attempt = OLD.attempt AND NEW.generation = OLD.generation AND NEW.sequence < OLD.sequence)
  OR (NEW.attempt > OLD.attempt AND NEW.generation <= OLD.generation) THEN
  RAISE EXCEPTION 'stale storage sync callback';
 END IF;
 IF NEW.attempt = OLD.attempt AND OLD.job_uid <> '' AND NEW.job_uid IS DISTINCT FROM OLD.job_uid THEN
  RAISE EXCEPTION 'storage sync attempt job identity is immutable';
 END IF;
 IF NEW.attempt = OLD.attempt AND COALESCE(OLD.snapshot_json->'private'->>'workerID', '') <> ''
  AND NEW.snapshot_json->'private'->>'workerID' IS DISTINCT FROM OLD.snapshot_json->'private'->>'workerID' THEN
  RAISE EXCEPTION 'storage sync attempt worker claim is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER storage_sync_run_fence BEFORE UPDATE ON storage_sync_runs FOR EACH ROW EXECUTE FUNCTION storage_sync_run_fence();

CREATE FUNCTION storage_sync_preview_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.plan_id, NEW.actor, NEW.created_at) IS DISTINCT FROM ROW(OLD.plan_id, OLD.actor, OLD.created_at)
  OR NEW.snapshot_json->'public'->'config' IS DISTINCT FROM OLD.snapshot_json->'public'->'config'
  OR NEW.snapshot_json->'private'->'resolved' IS DISTINCT FROM OLD.snapshot_json->'private'->'resolved' THEN
  RAISE EXCEPTION 'storage sync preview identity is immutable';
 END IF;
 IF NEW.attempt < OLD.attempt OR NEW.generation < OLD.generation
  OR (NEW.attempt = OLD.attempt AND NEW.generation = OLD.generation AND NEW.sequence < OLD.sequence) THEN
  RAISE EXCEPTION 'stale storage sync preview callback';
 END IF;
 IF NEW.attempt = OLD.attempt AND COALESCE(OLD.snapshot_json->'private'->>'workerID', '') <> ''
  AND NEW.snapshot_json->'private'->>'workerID' IS DISTINCT FROM OLD.snapshot_json->'private'->>'workerID' THEN
  RAISE EXCEPTION 'storage sync preview worker claim is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER storage_sync_preview_fence BEFORE UPDATE ON storage_sync_previews FOR EACH ROW EXECUTE FUNCTION storage_sync_preview_fence();
