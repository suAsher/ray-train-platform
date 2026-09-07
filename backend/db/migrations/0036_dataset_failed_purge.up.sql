SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Minimal identity reservations, not retained publication history or storage
-- metadata. There is deliberately no FK: deleting the dataset must not allow
-- an old publisher to reuse an already-purged version/run identity.
CREATE TABLE dataset_purge_identities (
 kind TEXT NOT NULL CHECK (kind IN ('version', 'run')),
 id TEXT NOT NULL,
 dataset_id TEXT NOT NULL,
 version TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (kind, id)
);
CREATE UNIQUE INDEX dataset_purge_version_identity_idx
 ON dataset_purge_identities(dataset_id, version) WHERE kind = 'version';

CREATE FUNCTION enforce_dataset_purge_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'purged dataset identity cannot be removed or changed' USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER dataset_purge_identity_immutable BEFORE UPDATE OR DELETE ON dataset_purge_identities
 FOR EACH ROW EXECUTE FUNCTION enforce_dataset_purge_identity_immutable();

CREATE FUNCTION enforce_dataset_purge_no_recreation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Acquire before reading identity reservations, not after waiting on an old
 -- row's unique key. Otherwise an INSERT started before purge could resurrect
 -- an ID once that row's DELETE commits.
 PERFORM pg_advisory_xact_lock_shared(hashtextextended('dataset-purge-identities', 34783));
 IF TG_TABLE_NAME = 'dataset_versions' THEN
  IF EXISTS (SELECT 1 FROM dataset_purge_identities WHERE kind = 'version'
    AND (id = NEW.id OR (dataset_id = NEW.dataset_id AND version = NEW.version))) THEN
   RAISE EXCEPTION 'purged dataset version cannot be recreated' USING ERRCODE = '23514';
  END IF;
 ELSE
  IF EXISTS (SELECT 1 FROM dataset_purge_identities WHERE
    (kind = 'run' AND id = NEW.id) OR (kind = 'version' AND id = NEW.dataset_version_id)) THEN
   RAISE EXCEPTION 'purged publication cannot be recreated' USING ERRCODE = '23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER dataset_versions_purge_identity_guard BEFORE INSERT OR UPDATE ON dataset_versions
 FOR EACH ROW EXECUTE FUNCTION enforce_dataset_purge_no_recreation();
CREATE TRIGGER dataset_publication_runs_purge_identity_guard BEFORE INSERT OR UPDATE ON dataset_publication_runs
 FOR EACH ROW EXECUTE FUNCTION enforce_dataset_purge_no_recreation();

CREATE FUNCTION fence_dataset_purge_storage_reference() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock_shared(hashtextextended('dataset-purge-identities', 34783));
 RETURN NEW;
END;
$$;
CREATE TRIGGER dataset_version_shards_purge_reference_guard BEFORE INSERT OR UPDATE ON dataset_version_shards
 FOR EACH ROW EXECUTE FUNCTION fence_dataset_purge_storage_reference();

-- Leave the existing UPDATE state machine and payload immutability unchanged.
DROP TRIGGER dataset_versions_immutability_guard ON dataset_versions;
CREATE TRIGGER dataset_versions_immutability_guard BEFORE UPDATE ON dataset_versions
 FOR EACH ROW EXECUTE FUNCTION enforce_dataset_version_immutability();
CREATE FUNCTION enforce_dataset_version_purge_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.state = 'RETIRED' THEN RETURN OLD; END IF;
 IF OLD.state = 'FAILED' AND EXISTS (SELECT 1 FROM dataset_purge_identities
   WHERE kind = 'version' AND id = OLD.id AND dataset_id = OLD.dataset_id AND version = OLD.version) THEN
  RETURN OLD;
 END IF;
 RAISE EXCEPTION 'dataset version must be retired or registered for failed purge before deletion' USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER dataset_versions_purge_delete_guard BEFORE DELETE ON dataset_versions
 FOR EACH ROW EXECUTE FUNCTION enforce_dataset_version_purge_delete();

CREATE OR REPLACE FUNCTION enforce_dataset_cleanup_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' AND OLD.state = 'FAILED' AND EXISTS (
  SELECT 1 FROM dataset_purge_identities WHERE id = OLD.id
   AND kind = CASE WHEN TG_TABLE_NAME = 'dataset_versions' THEN 'version' ELSE 'run' END
 ) THEN RETURN OLD; END IF;
 IF OLD.deleted_at IS NOT NULL THEN
  RAISE EXCEPTION 'deleted dataset record is immutable' USING ERRCODE = '23514';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 IF NEW.deleted_at IS NOT NULL AND (OLD.state <> 'FAILED' OR NEW.state <> 'FAILED') THEN
  RAISE EXCEPTION 'only failed dataset records can be hidden' USING ERRCODE = '23514';
 END IF;
 RETURN NEW;
END;
$$;
