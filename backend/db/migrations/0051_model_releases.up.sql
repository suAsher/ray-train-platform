SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS model_releases (
 id TEXT PRIMARY KEY,
 model_id TEXT NOT NULL REFERENCES model_catalog(id),
 version_id TEXT NOT NULL REFERENCES model_versions(id),
 model_sha256 TEXT NOT NULL CHECK (model_sha256 ~ '^[0-9a-f]{64}$'),
 model_owner_id TEXT NOT NULL CHECK (model_owner_id <> ''),
 evaluation_id TEXT NOT NULL REFERENCES model_evaluations(id),
 report_sha256 TEXT NOT NULL CHECK (report_sha256 ~ '^[0-9a-f]{64}$'),
 dataset_visibility TEXT NOT NULL CHECK (dataset_visibility IN ('PUBLIC','TEAM')),
 dataset_tenant_id TEXT NOT NULL DEFAULT '',
 applicant_id TEXT NOT NULL CHECK (applicant_id <> ''),
 applicant_name TEXT NOT NULL DEFAULT '',
 tenant_id TEXT NOT NULL CHECK (tenant_id <> ''),
 reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 4000),
 state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','APPROVED','REJECTED')),
 reviewer_id TEXT NOT NULL DEFAULT '',
 reviewer_name TEXT NOT NULL DEFAULT '',
 review_reason TEXT NOT NULL DEFAULT '' CHECK (length(review_reason) <= 4000),
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 reviewed_at TIMESTAMPTZ,
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 UNIQUE (applicant_id,tenant_id,idempotency_key),
 CHECK ((dataset_visibility='PUBLIC' AND dataset_tenant_id='') OR (dataset_visibility='TEAM' AND dataset_tenant_id<>'')),
 CHECK ((state='PENDING' AND reviewer_id='' AND reviewed_at IS NULL AND review_reason='') OR
        (state IN ('APPROVED','REJECTED') AND reviewer_id<>'' AND reviewed_at IS NOT NULL AND length(trim(review_reason))>0 AND reviewer_id<>applicant_id AND reviewer_id<>model_owner_id))
);
CREATE INDEX IF NOT EXISTS model_releases_model_idx ON model_releases(model_id,id);
CREATE INDEX IF NOT EXISTS model_releases_visibility_idx ON model_releases(dataset_visibility,dataset_tenant_id,id);
CREATE TABLE IF NOT EXISTS model_release_audits (
 id TEXT PRIMARY KEY,
 release_id TEXT NOT NULL REFERENCES model_releases(id),
 actor_id TEXT NOT NULL CHECK (actor_id<>''),
 action TEXT NOT NULL CHECK (action IN ('requested','approved','rejected')),
 reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 4000),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS model_publications (
 model_id TEXT PRIMARY KEY REFERENCES model_catalog(id),
 release_id TEXT NOT NULL REFERENCES model_releases(id),
 version_id TEXT NOT NULL REFERENCES model_versions(id),
 revision BIGINT NOT NULL CHECK (revision>=1),
 actor_id TEXT NOT NULL CHECK (actor_id<>''),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS model_publication_history (
 id TEXT PRIMARY KEY,
 model_id TEXT NOT NULL REFERENCES model_catalog(id),
 release_id TEXT NOT NULL REFERENCES model_releases(id),
 version_id TEXT NOT NULL REFERENCES model_versions(id),
 previous_release_id TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL CHECK (revision>=1),
 actor_id TEXT NOT NULL CHECK (actor_id<>''),
 actor_name TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 4000),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 UNIQUE(model_id,revision),
 UNIQUE(model_id,actor_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS model_publication_history_model_idx ON model_publication_history(model_id,id);

CREATE TABLE IF NOT EXISTS model_registry_links (
 version_id TEXT PRIMARY KEY REFERENCES model_versions(id),
 state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','SYNCING','READY','FAILED')),
 registered_name TEXT NOT NULL DEFAULT '',
 registry_version TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '',
 source_uri TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 lease_id TEXT NOT NULL DEFAULT '',
 lease_expires_at TIMESTAMPTZ,
 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision>=1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK (state<>'SYNCING' OR (lease_id<>'' AND lease_expires_at IS NOT NULL)),
 CHECK (state<>'READY' OR (registered_name<>'' AND registry_version<>'' AND source_uri<>''))
);
CREATE INDEX IF NOT EXISTS model_registry_links_pending_idx ON model_registry_links(state,lease_expires_at);

CREATE OR REPLACE FUNCTION model_releases_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.state<>'PENDING' AND to_jsonb(OLD) IS DISTINCT FROM to_jsonb(NEW) THEN
  RAISE EXCEPTION 'decided release is immutable';
 END IF;
 IF (to_jsonb(OLD)-ARRAY['state','reviewer_id','reviewer_name','review_reason','reviewed_at','revision','updated_at'])
    IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['state','reviewer_id','reviewer_name','review_reason','reviewed_at','revision','updated_at']) THEN
  RAISE EXCEPTION 'release evidence is immutable';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_releases_immutable ON model_releases;
CREATE TRIGGER model_releases_immutable BEFORE UPDATE ON model_releases FOR EACH ROW EXECUTE FUNCTION model_releases_immutable();

CREATE OR REPLACE FUNCTION model_release_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'release audit history is append only';
END;
$$;
DROP TRIGGER IF EXISTS model_release_audits_append_only ON model_release_audits;
CREATE TRIGGER model_release_audits_append_only BEFORE UPDATE OR DELETE ON model_release_audits FOR EACH ROW EXECUTE FUNCTION model_release_append_only();
DROP TRIGGER IF EXISTS model_publication_history_append_only ON model_publication_history;
CREATE TRIGGER model_publication_history_append_only BEFORE UPDATE OR DELETE ON model_publication_history FOR EACH ROW EXECUTE FUNCTION model_release_append_only();

CREATE OR REPLACE FUNCTION model_publication_approved() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM model_releases WHERE id=NEW.release_id AND model_id=NEW.model_id AND version_id=NEW.version_id AND state='APPROVED') THEN
  RAISE EXCEPTION 'publication must reference an approved release of the same model version';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS model_publication_approved ON model_publications;
CREATE TRIGGER model_publication_approved BEFORE INSERT OR UPDATE ON model_publications FOR EACH ROW EXECUTE FUNCTION model_publication_approved();
DROP TRIGGER IF EXISTS model_publication_history_approved ON model_publication_history;
CREATE TRIGGER model_publication_history_approved BEFORE INSERT ON model_publication_history FOR EACH ROW EXECUTE FUNCTION model_publication_approved();
