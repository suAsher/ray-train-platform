SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- A separate immutable namespace; no personal-data bindings or job records
-- are migrated or rewritten. READY records are the publication boundary.
CREATE UNIQUE INDEX mlflow_tracking_runs_artifact_owner ON mlflow_tracking_runs(id, tenant_id, user_id);
CREATE TABLE mlflow_tracking_artifacts (
 id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
 run_id TEXT NOT NULL,
 tenant_id TEXT NOT NULL,
 user_id TEXT NOT NULL,
 idempotency_hash TEXT NOT NULL CHECK (idempotency_hash ~ '^[0-9a-f]{64}$'),
 name TEXT NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 255 AND name NOT IN ('.','..') AND name !~ '[/\\\\[:cntrl:]]'),
 size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 21474836480),
 sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
 state TEXT NOT NULL CHECK (state IN ('PENDING','READY','CANCELLED')),
 part_size_bytes BIGINT NOT NULL DEFAULT 8388608 CHECK (part_size_bytes = 8388608),
 total_parts INTEGER NOT NULL CHECK (total_parts BETWEEN 1 AND 2560 AND total_parts = (size_bytes + 8388607) / 8388608),
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE (tenant_id, user_id, idempotency_hash),
 FOREIGN KEY (run_id, tenant_id, user_id) REFERENCES mlflow_tracking_runs(id, tenant_id, user_id) ON DELETE RESTRICT
);
CREATE INDEX mlflow_tracking_artifacts_owner_run ON mlflow_tracking_artifacts(tenant_id,user_id,run_id,id);
CREATE TABLE mlflow_tracking_artifact_parts (
 artifact_id TEXT NOT NULL REFERENCES mlflow_tracking_artifacts(id) ON DELETE RESTRICT,
 part_index INTEGER NOT NULL CHECK (part_index BETWEEN 1 AND 2560),
 size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 8388608),
 sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
 PRIMARY KEY (artifact_id,part_index)
);

CREATE FUNCTION guard_mlflow_tracking_artifact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'artifact records retain their immutable identity';
 END IF;
 IF ROW(NEW.id,NEW.run_id,NEW.tenant_id,NEW.user_id,NEW.idempotency_hash,NEW.name,NEW.size_bytes,NEW.sha256,NEW.part_size_bytes,NEW.total_parts,NEW.expires_at,NEW.created_at)
   IS DISTINCT FROM ROW(OLD.id,OLD.run_id,OLD.tenant_id,OLD.user_id,OLD.idempotency_hash,OLD.name,OLD.size_bytes,OLD.sha256,OLD.part_size_bytes,OLD.total_parts,OLD.expires_at,OLD.created_at) THEN
  RAISE EXCEPTION 'artifact identity and content declaration are immutable';
 END IF;
 IF OLD.state <> 'PENDING' AND NEW.state IS DISTINCT FROM OLD.state THEN
  RAISE EXCEPTION 'published and cancelled artifacts are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mlflow_tracking_artifact_immutable BEFORE UPDATE OR DELETE ON mlflow_tracking_artifacts FOR EACH ROW EXECUTE FUNCTION guard_mlflow_tracking_artifact();

CREATE FUNCTION guard_mlflow_tracking_artifact_part() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE parent mlflow_tracking_artifacts%ROWTYPE;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION 'registered artifact parts are immutable';
 END IF;
 SELECT * INTO parent FROM mlflow_tracking_artifacts WHERE id = NEW.artifact_id FOR UPDATE;
 IF parent.state <> 'PENDING' OR NEW.part_index > parent.total_parts OR
    NEW.size_bytes <> LEAST(parent.part_size_bytes, parent.size_bytes - (NEW.part_index - 1) * parent.part_size_bytes) THEN
  RAISE EXCEPTION 'invalid artifact part or immutable parent';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER mlflow_tracking_artifact_part_immutable BEFORE INSERT OR UPDATE OR DELETE ON mlflow_tracking_artifact_parts FOR EACH ROW EXECUTE FUNCTION guard_mlflow_tracking_artifact_part();
