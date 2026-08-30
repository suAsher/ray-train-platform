-- Dataset catalog and immutable-version provenance sit on the submission hot
-- path. Bound lock waits so a deployment fails instead of stalling backends.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS datasets (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  source_space TEXT NOT NULL,
  source_relative_path TEXT NOT NULL,
  owner_tenant_id TEXT REFERENCES tenants(id) ON DELETE RESTRICT,
  visibility TEXT NOT NULL,
  schema_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT datasets_id_check CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND id NOT IN ('.', '..')),
  CONSTRAINT datasets_slug_check CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$' AND slug NOT IN ('.', '..')),
  CONSTRAINT datasets_name_check CHECK (length(name) >= 1 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
  CONSTRAINT datasets_description_check CHECK (description !~ '[[:cntrl:]]'),
  CONSTRAINT datasets_source_space_check CHECK (source_space IN ('public', 'team-shared')),
  CONSTRAINT datasets_source_path_check CHECK (
    length(source_relative_path) BETWEEN 1 AND 4096
    AND source_relative_path = btrim(source_relative_path)
    AND source_relative_path <> '.'
    AND left(source_relative_path, 1) <> '/'
    AND source_relative_path NOT LIKE '//%'
    AND source_relative_path NOT LIKE '%//%'
    AND source_relative_path NOT LIKE './%'
    AND source_relative_path NOT LIKE '%/./%'
    AND source_relative_path NOT LIKE '%/.'
    AND source_relative_path NOT LIKE '../%'
    AND source_relative_path NOT LIKE '%/../%'
    AND source_relative_path NOT LIKE '%/..'
    AND position(chr(92) IN source_relative_path) = 0
    AND position('%' IN source_relative_path) = 0
    AND position('?' IN source_relative_path) = 0
    AND position('#' IN source_relative_path) = 0
    AND source_relative_path !~ '^[A-Za-z][A-Za-z0-9+.-]*:'
    AND source_relative_path !~ '[[:cntrl:]]'
  ),
  CONSTRAINT datasets_schema_version_check CHECK (schema_version ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND schema_version NOT IN ('.', '..')),
  CONSTRAINT datasets_visibility_check CHECK (visibility IN ('PUBLIC', 'TEAM')),
  CONSTRAINT datasets_visibility_owner_check CHECK (
    (visibility = 'PUBLIC' AND owner_tenant_id IS NULL AND source_space = 'public')
    OR (visibility = 'TEAM' AND owner_tenant_id IS NOT NULL AND source_space = 'team-shared')
  )
);

CREATE INDEX IF NOT EXISTS datasets_visibility_owner_idx
  ON datasets(visibility, owner_tenant_id, slug);
CREATE UNIQUE INDEX IF NOT EXISTS datasets_public_slug_uidx
  ON datasets(slug) WHERE visibility = 'PUBLIC';
CREATE UNIQUE INDEX IF NOT EXISTS datasets_team_slug_uidx
  ON datasets(owner_tenant_id, slug) WHERE visibility = 'TEAM';

CREATE TABLE IF NOT EXISTS dataset_versions (
  id TEXT PRIMARY KEY,
  dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  version TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'DISCOVERING',
  manifest_sha256 TEXT,
  manifest_object_key TEXT,
  schema_version TEXT NOT NULL,
  train_samples BIGINT NOT NULL DEFAULT 0,
  val_samples BIGINT NOT NULL DEFAULT 0,
  test_samples BIGINT NOT NULL DEFAULT 0,
  source_object_count BIGINT NOT NULL DEFAULT 0,
  logical_bytes BIGINT NOT NULL DEFAULT 0,
  packed_bytes BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (dataset_id, version),
  UNIQUE (id, dataset_id),
  CONSTRAINT dataset_versions_id_check CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND id NOT IN ('.', '..', 'latest')),
  CONSTRAINT dataset_versions_version_check CHECK (version ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND version NOT IN ('.', '..')),
  CONSTRAINT dataset_versions_state_check CHECK (state IN ('DISCOVERING', 'STABILIZING', 'VALIDATING', 'PACKING', 'READY', 'FAILED', 'DEPRECATED', 'RETIRED')),
  CONSTRAINT dataset_versions_manifest_digest_check CHECK (manifest_sha256 IS NULL OR manifest_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT dataset_versions_manifest_object_key_check CHECK (
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
      AND manifest_object_key = 'ray-train/platform/datasets/' || dataset_id || '/manifests/' || id || '.parquet'
    )
  ),
  CONSTRAINT dataset_versions_ready_manifest_check CHECK (
    state NOT IN ('READY', 'DEPRECATED', 'RETIRED')
    OR (manifest_sha256 IS NOT NULL AND manifest_object_key IS NOT NULL)
  ),
  CONSTRAINT dataset_versions_schema_version_check CHECK (schema_version ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND schema_version NOT IN ('.', '..')),
  CONSTRAINT dataset_versions_counts_check CHECK (
    train_samples >= 0
    AND val_samples >= 0
    AND test_samples >= 0
    AND source_object_count >= 0
    AND logical_bytes >= 0
    AND packed_bytes >= 0
  )
);

CREATE INDEX IF NOT EXISTS dataset_versions_ready_latest_idx
  ON dataset_versions(dataset_id, created_at DESC, id DESC)
  WHERE state = 'READY';
CREATE INDEX IF NOT EXISTS dataset_versions_state_idx
  ON dataset_versions(state, updated_at DESC, id);

CREATE OR REPLACE FUNCTION enforce_dataset_version_immutability()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.state <> 'RETIRED' THEN
      RAISE EXCEPTION 'dataset version % must be RETIRED before deletion', OLD.id
        USING ERRCODE = '23514';
    END IF;
    RETURN OLD;
  END IF;

  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
     OR NEW.version IS DISTINCT FROM OLD.version
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'dataset version identity is immutable'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
    (OLD.state = 'DISCOVERING' AND NEW.state IN ('STABILIZING', 'FAILED'))
    OR (OLD.state = 'STABILIZING' AND NEW.state IN ('VALIDATING', 'FAILED'))
    OR (OLD.state = 'VALIDATING' AND NEW.state IN ('PACKING', 'FAILED'))
    OR (OLD.state = 'PACKING' AND NEW.state IN ('READY', 'FAILED'))
    OR (OLD.state = 'FAILED' AND NEW.state = 'DISCOVERING')
    OR (OLD.state = 'READY' AND NEW.state = 'DEPRECATED')
    OR (OLD.state = 'DEPRECATED' AND NEW.state = 'RETIRED')
  ) THEN
    RAISE EXCEPTION 'invalid dataset version transition from % to %', OLD.state, NEW.state
      USING ERRCODE = '23514';
  END IF;

  IF OLD.state IN ('READY', 'DEPRECATED', 'RETIRED') AND (
    NEW.manifest_sha256 IS DISTINCT FROM OLD.manifest_sha256
    OR NEW.manifest_object_key IS DISTINCT FROM OLD.manifest_object_key
    OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
    OR NEW.train_samples IS DISTINCT FROM OLD.train_samples
    OR NEW.val_samples IS DISTINCT FROM OLD.val_samples
    OR NEW.test_samples IS DISTINCT FROM OLD.test_samples
    OR NEW.source_object_count IS DISTINCT FROM OLD.source_object_count
    OR NEW.logical_bytes IS DISTINCT FROM OLD.logical_bytes
    OR NEW.packed_bytes IS DISTINCT FROM OLD.packed_bytes
  ) THEN
    RAISE EXCEPTION 'published dataset version payload is immutable'
      USING ERRCODE = '23514';
  END IF;

  RETURN NEW;
END
$$;

CREATE TRIGGER dataset_versions_immutability_guard
  BEFORE UPDATE OR DELETE ON dataset_versions
  FOR EACH ROW EXECUTE FUNCTION enforce_dataset_version_immutability();

CREATE OR REPLACE FUNCTION enforce_dataset_version_child_immutability()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  version_id TEXT;
  version_state TEXT;
BEGIN
  IF TG_OP = 'UPDATE' AND NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id THEN
    RAISE EXCEPTION 'dataset version child cannot move between versions'
      USING ERRCODE = '23514';
  END IF;

  IF TG_OP = 'DELETE' THEN
    version_id := OLD.dataset_version_id;
  ELSE
    version_id := NEW.dataset_version_id;
  END IF;

  SELECT state INTO version_state FROM dataset_versions WHERE id = version_id;
  IF NOT FOUND THEN
    -- A parent RETIRED version has already been removed by its cascading
    -- delete. Foreign keys reject missing parents for INSERT/UPDATE.
    IF TG_OP = 'DELETE' THEN
      RETURN OLD;
    END IF;
    RETURN NEW;
  END IF;

  IF version_state IN ('READY', 'DEPRECATED')
     OR (version_state = 'RETIRED' AND TG_OP <> 'DELETE') THEN
    RAISE EXCEPTION 'published dataset version children are immutable'
      USING ERRCODE = '23514';
  END IF;

  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

CREATE TABLE IF NOT EXISTS dataset_partitions (
  id TEXT PRIMARY KEY,
  dataset_version_id TEXT NOT NULL REFERENCES dataset_versions(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  source_object_count BIGINT NOT NULL DEFAULT 0,
  processed_object_count BIGINT NOT NULL DEFAULT 0,
  failed_object_count BIGINT NOT NULL DEFAULT 0,
  logical_bytes BIGINT NOT NULL DEFAULT 0,
  packed_bytes BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (id, dataset_version_id),
  UNIQUE (dataset_version_id, name),
  CONSTRAINT dataset_partitions_id_check CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND id NOT IN ('.', '..')),
  CONSTRAINT dataset_partitions_name_check CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND name NOT IN ('.', '..')),
  CONSTRAINT dataset_partitions_progress_check CHECK (
    source_object_count >= 0
    AND processed_object_count >= 0
    AND failed_object_count >= 0
    AND processed_object_count <= source_object_count
    AND failed_object_count <= source_object_count - processed_object_count
    AND logical_bytes >= 0
    AND packed_bytes >= 0
  )
);

CREATE INDEX IF NOT EXISTS dataset_partitions_version_idx
  ON dataset_partitions(dataset_version_id, updated_at DESC, id);

CREATE TRIGGER dataset_partitions_immutability_guard
  BEFORE INSERT OR UPDATE OR DELETE ON dataset_partitions
  FOR EACH ROW EXECUTE FUNCTION enforce_dataset_version_child_immutability();

CREATE TABLE IF NOT EXISTS dataset_publication_runs (
  id TEXT PRIMARY KEY,
  dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  dataset_version_id TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'DISCOVERING',
  total_partitions BIGINT NOT NULL DEFAULT 0,
  completed_partitions BIGINT NOT NULL DEFAULT 0,
  failed_partitions BIGINT NOT NULL DEFAULT 0,
  source_object_count BIGINT NOT NULL DEFAULT 0,
  processed_object_count BIGINT NOT NULL DEFAULT 0,
  failed_object_count BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  CONSTRAINT dataset_publication_runs_version_fk
    FOREIGN KEY (dataset_version_id, dataset_id) REFERENCES dataset_versions(id, dataset_id) ON DELETE CASCADE,
  CONSTRAINT dataset_publication_runs_id_check CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND id NOT IN ('.', '..')),
  CONSTRAINT dataset_publication_runs_state_check CHECK (state IN ('DISCOVERING', 'STABILIZING', 'VALIDATING', 'PACKING', 'READY', 'FAILED', 'DEPRECATED', 'RETIRED')),
  CONSTRAINT dataset_publication_runs_partition_progress_check CHECK (
    total_partitions >= 0
    AND completed_partitions >= 0
    AND failed_partitions >= 0
    AND completed_partitions <= total_partitions
    AND failed_partitions <= total_partitions - completed_partitions
  ),
  CONSTRAINT dataset_publication_runs_object_progress_check CHECK (
    source_object_count >= 0
    AND processed_object_count >= 0
    AND failed_object_count >= 0
    AND processed_object_count <= source_object_count
    AND failed_object_count <= source_object_count - processed_object_count
  )
);

CREATE INDEX IF NOT EXISTS dataset_publication_runs_dataset_state_idx
  ON dataset_publication_runs(dataset_id, state, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS dataset_publication_runs_version_state_idx
  ON dataset_publication_runs(dataset_version_id, state, updated_at DESC, id);

CREATE TABLE IF NOT EXISTS dataset_version_shards (
  dataset_version_id TEXT NOT NULL REFERENCES dataset_versions(id) ON DELETE CASCADE,
  dataset_id TEXT NOT NULL,
  partition_id TEXT NOT NULL,
  split TEXT NOT NULL,
  ordinal BIGINT NOT NULL,
  shard_sha256 TEXT NOT NULL,
  object_key TEXT NOT NULL,
  sample_count BIGINT NOT NULL DEFAULT 0,
  logical_bytes BIGINT NOT NULL DEFAULT 0,
  packed_bytes BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (dataset_version_id, shard_sha256),
  UNIQUE (dataset_version_id, split, ordinal),
  CONSTRAINT dataset_version_shards_partition_fk
    FOREIGN KEY (partition_id, dataset_version_id) REFERENCES dataset_partitions(id, dataset_version_id) ON DELETE CASCADE,
  CONSTRAINT dataset_version_shards_dataset_fk
    FOREIGN KEY (dataset_version_id, dataset_id) REFERENCES dataset_versions(id, dataset_id) ON DELETE CASCADE,
  CONSTRAINT dataset_version_shards_digest_check CHECK (shard_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT dataset_version_shards_split_check CHECK (split IN ('train', 'val', 'test')),
  CONSTRAINT dataset_version_shards_object_key_check CHECK (
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
    AND object_key = 'ray-train/platform/datasets/' || dataset_id || '/objects/sha256/' || left(shard_sha256, 2) || '/' || shard_sha256 || '.parquet'
  ),
  CONSTRAINT dataset_version_shards_counts_check CHECK (
    ordinal >= 0
    AND sample_count >= 0
    AND logical_bytes >= 0
    AND packed_bytes >= 0
  )
);

CREATE INDEX IF NOT EXISTS dataset_version_shards_version_split_idx
  ON dataset_version_shards(dataset_version_id, split, ordinal);
CREATE INDEX IF NOT EXISTS dataset_version_shards_digest_idx
  ON dataset_version_shards(shard_sha256, object_key);

CREATE TRIGGER dataset_version_shards_immutability_guard
  BEFORE INSERT OR UPDATE OR DELETE ON dataset_version_shards
  FOR EACH ROW EXECUTE FUNCTION enforce_dataset_version_child_immutability();

-- Each column remains nullable so pre-versioning jobs and jobs submitted by an
-- older backend during a rolling deployment continue to insert all-NULL
-- provenance. Version-aware writers must set the complete tuple.
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS dataset_id TEXT;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS dataset_version_id TEXT;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS dataset_manifest_digest TEXT;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS dataset_data_mode TEXT;
ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS dataset_cache_policy TEXT;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'training_jobs_dataset_manifest_digest_check'
      AND conrelid = 'training_jobs'::regclass
  ) THEN
    ALTER TABLE training_jobs
      ADD CONSTRAINT training_jobs_dataset_manifest_digest_check
      CHECK (dataset_manifest_digest IS NULL OR dataset_manifest_digest ~ '^[0-9a-f]{64}$');
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'training_jobs_dataset_data_mode_check'
      AND conrelid = 'training_jobs'::regclass
  ) THEN
    ALTER TABLE training_jobs
      ADD CONSTRAINT training_jobs_dataset_data_mode_check
      CHECK (dataset_data_mode IS NULL OR dataset_data_mode = 'streaming');
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'training_jobs_dataset_cache_policy_check'
      AND conrelid = 'training_jobs'::regclass
  ) THEN
    ALTER TABLE training_jobs
      ADD CONSTRAINT training_jobs_dataset_cache_policy_check
      CHECK (dataset_cache_policy IS NULL OR dataset_cache_policy IN ('auto', 'off', 'bounded'));
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'training_jobs_dataset_provenance_check'
      AND conrelid = 'training_jobs'::regclass
  ) THEN
    ALTER TABLE training_jobs
      ADD CONSTRAINT training_jobs_dataset_provenance_check
      CHECK (
        (
          dataset_id IS NULL
          AND dataset_version_id IS NULL
          AND dataset_manifest_digest IS NULL
          AND dataset_data_mode IS NULL
          AND dataset_cache_policy IS NULL
        ) OR (
          dataset_id IS NOT NULL
          AND dataset_version_id IS NOT NULL
          AND dataset_manifest_digest IS NOT NULL
          AND dataset_data_mode IS NOT NULL
          AND dataset_cache_policy IS NOT NULL
        )
      );
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'training_jobs_dataset_version_fk'
      AND conrelid = 'training_jobs'::regclass
  ) THEN
    ALTER TABLE training_jobs
      ADD CONSTRAINT training_jobs_dataset_version_fk
      FOREIGN KEY (dataset_version_id, dataset_id) REFERENCES dataset_versions(id, dataset_id) ON DELETE RESTRICT;
  END IF;
END
$$;

CREATE INDEX IF NOT EXISTS training_jobs_dataset_version_idx
  ON training_jobs(dataset_version_id, dataset_id)
  WHERE dataset_version_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS training_jobs_id_dataset_version_uidx
  ON training_jobs(id, dataset_version_id);

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
      USING ERRCODE = '23503';
  END IF;
  IF version_state <> 'READY' THEN
    RAISE EXCEPTION 'training jobs can only pin READY dataset versions'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.dataset_manifest_digest IS DISTINCT FROM version_manifest_sha256 THEN
    RAISE EXCEPTION 'training job manifest digest does not match its dataset version'
      USING ERRCODE = '23514';
  END IF;
  IF dataset_visibility = 'TEAM' AND dataset_owner_tenant_id IS DISTINCT FROM NEW.tenant_id THEN
    RAISE EXCEPTION 'training job tenant cannot access TEAM dataset version'
      USING ERRCODE = '23514';
  END IF;

  RETURN NEW;
END
$$;

CREATE TRIGGER training_jobs_dataset_scope_guard
  BEFORE INSERT OR UPDATE ON training_jobs
  FOR EACH ROW EXECUTE FUNCTION enforce_training_job_dataset_scope();

CREATE TABLE IF NOT EXISTS dataset_cache_observations (
  id TEXT PRIMARY KEY,
  dataset_version_id TEXT NOT NULL REFERENCES dataset_versions(id) ON DELETE CASCADE,
  training_job_id TEXT NOT NULL,
  node_name TEXT NOT NULL,
  cache_hit_count BIGINT NOT NULL DEFAULT 0,
  cache_miss_count BIGINT NOT NULL DEFAULT 0,
  cache_hit_bytes BIGINT NOT NULL DEFAULT 0,
  cache_miss_bytes BIGINT NOT NULL DEFAULT 0,
  cached_bytes BIGINT NOT NULL DEFAULT 0,
  evicted_bytes BIGINT NOT NULL DEFAULT 0,
  checksum_failure_count BIGINT NOT NULL DEFAULT 0,
  prefetch_wait_milliseconds BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT dataset_cache_observations_job_version_fk
    FOREIGN KEY (training_job_id, dataset_version_id) REFERENCES training_jobs(id, dataset_version_id) ON DELETE CASCADE,
  CONSTRAINT dataset_cache_observations_id_check CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$' AND id NOT IN ('.', '..')),
  CONSTRAINT dataset_cache_observations_node_check CHECK (length(node_name) BETWEEN 1 AND 128 AND node_name = btrim(node_name) AND node_name !~ '[[:cntrl:]]'),
  CONSTRAINT dataset_cache_observations_counters_check CHECK (
    cache_hit_count >= 0
    AND cache_miss_count >= 0
    AND cache_hit_bytes >= 0
    AND cache_miss_bytes >= 0
    AND cached_bytes >= 0
    AND evicted_bytes >= 0
    AND checksum_failure_count >= 0
    AND prefetch_wait_milliseconds >= 0
  )
);

CREATE INDEX IF NOT EXISTS dataset_cache_observations_job_idx
  ON dataset_cache_observations(training_job_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS dataset_cache_observations_version_idx
  ON dataset_cache_observations(dataset_version_id, created_at DESC, id);
