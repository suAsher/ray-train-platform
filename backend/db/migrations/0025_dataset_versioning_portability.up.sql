SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 0024 originally bound internal objects to one deployment prefix. Rebuild
-- only the key-shape constraints so upgraded clusters can use the configured
-- private prefix while preserving dataset/version/content-address ownership.
ALTER TABLE dataset_versions
  DROP CONSTRAINT IF EXISTS dataset_versions_manifest_object_key_check;

ALTER TABLE dataset_versions
  ADD CONSTRAINT dataset_versions_manifest_object_key_check CHECK (
    manifest_object_key IS NULL OR (
      length(manifest_object_key) BETWEEN 1 AND 4096
      AND manifest_object_key = btrim(manifest_object_key)
      AND left(manifest_object_key, 1) <> '/'
      AND manifest_object_key NOT LIKE '%//%'
      AND manifest_object_key NOT LIKE './%'
      AND manifest_object_key NOT LIKE '%/./%'
      AND manifest_object_key NOT LIKE '%/.'
      AND manifest_object_key NOT LIKE '../%'
      AND manifest_object_key NOT LIKE '%/../%'
      AND manifest_object_key NOT LIKE '%/..'
      AND position(chr(92) IN manifest_object_key) = 0
      AND position('%' IN manifest_object_key) = 0
      AND position('?' IN manifest_object_key) = 0
      AND position('#' IN manifest_object_key) = 0
      AND manifest_object_key !~ '^[A-Za-z][A-Za-z0-9+.-]*:'
      AND manifest_object_key !~ '[[:cntrl:]]'
      AND length(manifest_object_key) > length('/' || dataset_id || '/manifests/' || id || '.parquet')
      AND right(manifest_object_key, length('/' || dataset_id || '/manifests/' || id || '.parquet')) = '/' || dataset_id || '/manifests/' || id || '.parquet'
    )
  );

ALTER TABLE dataset_version_shards
  DROP CONSTRAINT IF EXISTS dataset_version_shards_object_key_check;

ALTER TABLE dataset_version_shards
  ADD CONSTRAINT dataset_version_shards_object_key_check CHECK (
    length(object_key) BETWEEN 1 AND 4096
    AND object_key = btrim(object_key)
    AND left(object_key, 1) <> '/'
    AND object_key NOT LIKE '%//%'
    AND object_key NOT LIKE './%'
    AND object_key NOT LIKE '%/./%'
    AND object_key NOT LIKE '%/.'
    AND object_key NOT LIKE '../%'
    AND object_key NOT LIKE '%/../%'
    AND object_key NOT LIKE '%/..'
    AND position(chr(92) IN object_key) = 0
    AND position('%' IN object_key) = 0
    AND position('?' IN object_key) = 0
    AND position('#' IN object_key) = 0
    AND object_key !~ '^[A-Za-z][A-Za-z0-9+.-]*:'
    AND object_key !~ '[[:cntrl:]]'
    AND length(object_key) > length('/' || dataset_id || '/objects/sha256/' || left(shard_sha256, 2) || '/' || shard_sha256 || '.parquet')
    AND right(object_key, length('/' || dataset_id || '/objects/sha256/' || left(shard_sha256, 2) || '/' || shard_sha256 || '.parquet')) = '/' || dataset_id || '/objects/sha256/' || left(shard_sha256, 2) || '/' || shard_sha256 || '.parquet'
  );

-- A custom SQLSTATE is deliberately outside GORM's translated 23503/23514
-- set, retaining the trigger message so the API can distinguish a READY
-- version race from unrelated training_jobs constraints.
CREATE OR REPLACE FUNCTION enforce_training_job_dataset_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  version_state TEXT;
  version_manifest_sha256 TEXT;
  dataset_visibility TEXT;
  dataset_owner_tenant_id TEXT;
BEGIN
  IF TG_OP = 'UPDATE'
     AND NEW.tenant_id IS NOT DISTINCT FROM OLD.tenant_id
     AND NEW.dataset_id IS NOT DISTINCT FROM OLD.dataset_id
     AND NEW.dataset_version_id IS NOT DISTINCT FROM OLD.dataset_version_id
     AND NEW.dataset_manifest_digest IS NOT DISTINCT FROM OLD.dataset_manifest_digest
     AND NEW.dataset_data_mode IS NOT DISTINCT FROM OLD.dataset_data_mode
     AND NEW.dataset_cache_policy IS NOT DISTINCT FROM OLD.dataset_cache_policy THEN
    RETURN NEW;
  END IF;

  IF NEW.dataset_version_id IS NULL THEN
    RETURN NEW;
  END IF;

  SELECT version.state, version.manifest_sha256, dataset.visibility, dataset.owner_tenant_id
  INTO version_state, version_manifest_sha256, dataset_visibility, dataset_owner_tenant_id
  FROM dataset_versions AS version
  JOIN datasets AS dataset ON dataset.id = version.dataset_id
  WHERE version.id = NEW.dataset_version_id
    AND version.dataset_id = NEW.dataset_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'training job dataset version does not exist'
      USING ERRCODE = 'P0001';
  END IF;
  IF version_state <> 'READY' THEN
    RAISE EXCEPTION 'training jobs can only pin READY dataset versions'
      USING ERRCODE = 'P0001';
  END IF;
  IF NEW.dataset_manifest_digest IS DISTINCT FROM version_manifest_sha256 THEN
    RAISE EXCEPTION 'training job manifest digest does not match its dataset version'
      USING ERRCODE = 'P0001';
  END IF;
  IF dataset_visibility = 'TEAM' AND dataset_owner_tenant_id IS DISTINCT FROM NEW.tenant_id THEN
    RAISE EXCEPTION 'training job tenant cannot access TEAM dataset version'
      USING ERRCODE = 'P0001';
  END IF;

  RETURN NEW;
END
$$;
