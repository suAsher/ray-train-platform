SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Empty is the legacy full-version selection. Canonical JSON is validated at
-- the API and checked against spec_json when decoding a persisted job.
ALTER TABLE training_jobs ADD COLUMN dataset_sites TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD CONSTRAINT training_jobs_dataset_sites_requires_version
  CHECK (dataset_sites = '' OR dataset_version_id IS NOT NULL);

CREATE FUNCTION prevent_training_dataset_sites_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.dataset_sites IS DISTINCT FROM OLD.dataset_sites THEN
    RAISE EXCEPTION 'training dataset site selection is immutable';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER training_dataset_sites_immutable BEFORE UPDATE ON training_jobs
  FOR EACH ROW EXECUTE FUNCTION prevent_training_dataset_sites_update();
