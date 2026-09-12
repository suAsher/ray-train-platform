SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Stable personal ownership is deliberately independent of current memberships.
-- Source jobs/users may be retired without removing a published model snapshot.
CREATE TABLE IF NOT EXISTS model_catalog (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
 description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 4000),
 owner_id TEXT NOT NULL CHECK (owner_id <> ''),
 owner_name TEXT NOT NULL DEFAULT '',
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 archived BOOLEAN NOT NULL DEFAULT FALSE,
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_catalog_owner_idx ON model_catalog(owner_id, archived, id);
CREATE INDEX IF NOT EXISTS model_catalog_list_idx ON model_catalog(archived, id);

CREATE TABLE IF NOT EXISTS model_versions (
 id TEXT PRIMARY KEY,
 model_id TEXT NOT NULL REFERENCES model_catalog(id),
 number BIGINT NOT NULL CHECK (number >= 1),
 description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 4000),
 creator_id TEXT NOT NULL CHECK (creator_id <> ''),
 creator_name TEXT NOT NULL DEFAULT '',
 job_id TEXT NOT NULL CHECK (job_id <> ''),
 job_name TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '',
 file_name TEXT NOT NULL CHECK (length(file_name) BETWEEN 1 AND 255),
 source_root TEXT NOT NULL CHECK (source_root <> ''),
 relative_path TEXT NOT NULL CHECK (relative_path <> ''),
 code_sha256 TEXT NOT NULL DEFAULT '' CHECK (code_sha256 = '' OR code_sha256 ~ '^[0-9a-f]{64}$'),
 code_commit TEXT NOT NULL DEFAULT '' CHECK (code_commit = '' OR code_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
 runtime_image TEXT NOT NULL DEFAULT '' CHECK (length(runtime_image) <= 512),
 dataset_id TEXT NOT NULL DEFAULT '',
 dataset_version_id TEXT NOT NULL DEFAULT '',
 dataset_name TEXT NOT NULL DEFAULT '',
 dataset_manifest_sha256 TEXT NOT NULL DEFAULT '' CHECK (dataset_manifest_sha256 = '' OR dataset_manifest_sha256 ~ '^[0-9a-f]{64}$'),
 dataset_association TEXT NOT NULL DEFAULT '' CHECK (dataset_association IN ('', 'training-record', 'user-declared')),
 size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 21474836480),
 sha256 TEXT NOT NULL DEFAULT '' CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
 state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'COPYING', 'READY', 'FAILED')),
 error TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 lease_id TEXT NOT NULL DEFAULT '',
 lease_expires_at TIMESTAMPTZ,
 parts JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(parts) = 'array' AND jsonb_array_length(parts) <= 2560),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE (model_id, number),
 UNIQUE (model_id, creator_id, idempotency_key),
 CHECK (state <> 'COPYING' OR (lease_id <> '' AND lease_expires_at IS NOT NULL)),
 CHECK (state <> 'READY' OR (sha256 <> '' AND jsonb_array_length(parts) > 0)),
 CHECK ((dataset_association = '' AND dataset_id = '' AND dataset_version_id = '' AND dataset_manifest_sha256 = '') OR
        (dataset_association <> '' AND dataset_id <> '' AND dataset_version_id <> '' AND dataset_manifest_sha256 <> ''))
);
CREATE INDEX IF NOT EXISTS model_versions_list_idx ON model_versions(model_id, id);
CREATE INDEX IF NOT EXISTS model_versions_worker_idx ON model_versions(created_at, id) WHERE state = 'PENDING';
CREATE INDEX IF NOT EXISTS model_versions_expiry_idx ON model_versions(lease_expires_at) WHERE state = 'COPYING';

CREATE TABLE IF NOT EXISTS model_audits (
 id TEXT PRIMARY KEY,
 model_id TEXT NOT NULL REFERENCES model_catalog(id),
 version_id TEXT NOT NULL DEFAULT '',
 actor_id TEXT NOT NULL,
 actor_name TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL,
 details JSONB NOT NULL DEFAULT '{}'::jsonb,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS model_audits_model_idx ON model_audits(model_id, created_at);

CREATE OR REPLACE FUNCTION model_versions_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(OLD) - ARRAY['description','revision','updated_at','state','error','lease_id','lease_expires_at','parts','sha256'])
    IS DISTINCT FROM
    (to_jsonb(NEW) - ARRAY['description','revision','updated_at','state','error','lease_id','lease_expires_at','parts','sha256']) THEN
  RAISE EXCEPTION 'model version provenance is immutable';
 END IF;
 IF OLD.state IN ('READY','FAILED') AND
    ROW(NEW.state,NEW.sha256,NEW.parts,NEW.error,NEW.lease_id,NEW.lease_expires_at)
    IS DISTINCT FROM ROW(OLD.state,OLD.sha256,OLD.parts,OLD.error,OLD.lease_id,OLD.lease_expires_at) THEN
  RAISE EXCEPTION 'terminal model snapshot is immutable';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_versions_immutable ON model_versions;
CREATE TRIGGER model_versions_immutable BEFORE UPDATE ON model_versions FOR EACH ROW EXECUTE FUNCTION model_versions_immutable();
