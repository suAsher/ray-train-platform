SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Tombstones retain every primary/unique key and storage reference.
ALTER TABLE dataset_versions ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE dataset_versions ADD COLUMN deleted_by TEXT NOT NULL DEFAULT '';
ALTER TABLE dataset_publication_runs ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE dataset_publication_runs ADD COLUMN deleted_by TEXT NOT NULL DEFAULT '';
CREATE INDEX dataset_versions_deleted_at_idx ON dataset_versions(deleted_at);
CREATE INDEX dataset_publication_runs_deleted_at_idx ON dataset_publication_runs(deleted_at);
ALTER TABLE dataset_versions ADD CONSTRAINT dataset_versions_cleanup_audit_check
  CHECK ((deleted_at IS NULL AND deleted_by = '') OR (deleted_at IS NOT NULL AND btrim(deleted_by) <> '' AND state = 'FAILED'));
ALTER TABLE dataset_publication_runs ADD CONSTRAINT dataset_publication_runs_cleanup_audit_check
  CHECK ((deleted_at IS NULL AND deleted_by = '') OR (deleted_at IS NOT NULL AND btrim(deleted_by) <> '' AND state = 'FAILED'));

CREATE FUNCTION enforce_dataset_cleanup_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
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
CREATE TRIGGER dataset_versions_cleanup_guard BEFORE UPDATE OR DELETE ON dataset_versions
  FOR EACH ROW EXECUTE FUNCTION enforce_dataset_cleanup_tombstone();
CREATE TRIGGER dataset_publication_runs_cleanup_guard BEFORE UPDATE OR DELETE ON dataset_publication_runs
  FOR EACH ROW EXECUTE FUNCTION enforce_dataset_cleanup_tombstone();

-- Worker SQL uses this table directly. Serialize new/renewed work with cleanup
-- so a lease cannot appear between the dependency check and the tombstone.
CREATE FUNCTION enforce_dataset_attempt_cleanup_scope() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  parent_deleted_at TIMESTAMPTZ;
BEGIN
  SELECT deleted_at INTO parent_deleted_at FROM dataset_versions
    WHERE id = NEW.dataset_version_id FOR UPDATE;
  IF NOT FOUND OR parent_deleted_at IS NOT NULL OR EXISTS (
    SELECT 1 FROM dataset_publication_runs
    WHERE dataset_version_id = NEW.dataset_version_id AND deleted_at IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'publication attempt parent is deleted' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER dataset_publication_attempts_cleanup_guard
  BEFORE INSERT OR UPDATE ON dataset_publication_partition_attempts
  FOR EACH ROW EXECUTE FUNCTION enforce_dataset_attempt_cleanup_scope();
