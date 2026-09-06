SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE tenants ADD COLUMN retired_at TIMESTAMPTZ;
ALTER TABLE tenants ADD COLUMN retired_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tenants ADD CONSTRAINT tenants_retirement_pair CHECK
 ((retired_at IS NULL AND retired_by = '') OR (retired_at IS NOT NULL AND btrim(retired_by) <> ''));

-- The advisory fence is also held around HTTP side effects. Taking it before
-- the row lock makes credential creation and retirement serialize consistently.
CREATE FUNCTION require_active_tenant_write() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_tenant_id TEXT; new_tenant_id TEXT; candidate_id TEXT; retired TIMESTAMPTZ;
BEGIN
  IF TG_OP <> 'INSERT' THEN old_tenant_id := to_jsonb(OLD) ->> TG_ARGV[0]; END IF;
  IF TG_OP <> 'DELETE' THEN new_tenant_id := to_jsonb(NEW) ->> TG_ARGV[0]; END IF;
  FOR candidate_id IN
    SELECT DISTINCT tenant_id
    FROM unnest(ARRAY[old_tenant_id, new_tenant_id]) AS candidates(tenant_id)
    WHERE tenant_id IS NOT NULL AND tenant_id <> ''
    ORDER BY tenant_id
  LOOP
    PERFORM pg_advisory_xact_lock_shared(hashtextextended(candidate_id, 34781));
    SELECT retired_at INTO retired FROM tenants WHERE id = candidate_id FOR SHARE;
    IF retired IS NOT NULL THEN
      RAISE EXCEPTION 'tenant is retired' USING ERRCODE = '23514';
    END IF;
  END LOOP;
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

DO $$
DECLARE target RECORD;
BEGIN
 FOR target IN SELECT table_name, column_name FROM information_schema.columns
  WHERE table_schema = current_schema() AND column_name IN ('tenant_id','owner_tenant_id')
    AND table_name NOT IN ('audit_logs')
 LOOP
  EXECUTE format('CREATE TRIGGER tenant_lifecycle_write_guard BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION require_active_tenant_write(%L)', target.table_name, target.column_name);
 END LOOP;
END;
$$;

CREATE FUNCTION preserve_retired_tenant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.retired_at IS NOT NULL THEN
  RAISE EXCEPTION 'retired tenant identity is permanent' USING ERRCODE = '23514';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER tenants_retirement_guard BEFORE UPDATE OR DELETE ON tenants
 FOR EACH ROW EXECUTE FUNCTION preserve_retired_tenant();

-- Dataset children carry a dataset/version identity instead of tenant_id.
CREATE FUNCTION require_active_dataset_tenant_write() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE payload JSONB; dataset_identity TEXT; target_id TEXT; retired TIMESTAMPTZ;
BEGIN
 IF TG_OP = 'DELETE' THEN payload := to_jsonb(OLD); ELSE payload := to_jsonb(NEW); END IF;
 dataset_identity := payload ->> 'dataset_id';
 IF dataset_identity IS NULL THEN
  SELECT dataset_id INTO dataset_identity FROM dataset_versions WHERE id = payload ->> 'dataset_version_id';
 END IF;
 SELECT owner_tenant_id INTO target_id FROM datasets WHERE id = dataset_identity;
 IF target_id IS NOT NULL THEN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended(target_id, 34781));
  SELECT retired_at INTO retired FROM tenants WHERE id = target_id FOR SHARE;
  IF retired IS NOT NULL THEN RAISE EXCEPTION 'tenant is retired' USING ERRCODE = '23514'; END IF;
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
DO $$
DECLARE table_name TEXT;
BEGIN
 FOREACH table_name IN ARRAY ARRAY['dataset_versions','dataset_partitions','dataset_publication_runs','dataset_publication_partition_attempts','dataset_version_shards','dataset_cache_observations'] LOOP
  EXECUTE format('CREATE TRIGGER dataset_tenant_lifecycle_write_guard BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION require_active_dataset_tenant_write()', table_name);
 END LOOP;
END;
$$;

-- Children without tenant_id must fence the owner of both OLD and NEW parents.
-- Parent identifiers/table names are fixed migration arguments, never input.
CREATE FUNCTION require_active_parent_tenant_write() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_parent TEXT; new_parent TEXT; candidate_id TEXT; retired TIMESTAMPTZ;
BEGIN
 IF TG_OP <> 'INSERT' THEN old_parent := to_jsonb(OLD) ->> TG_ARGV[1]; END IF;
 IF TG_OP <> 'DELETE' THEN new_parent := to_jsonb(NEW) ->> TG_ARGV[1]; END IF;
 FOR candidate_id IN EXECUTE format('SELECT DISTINCT tenant_id FROM %I WHERE id = ANY($1) ORDER BY tenant_id', TG_ARGV[0]) USING ARRAY[old_parent,new_parent]
 LOOP
  PERFORM pg_advisory_xact_lock_shared(hashtextextended(candidate_id, 34781));
  SELECT retired_at INTO retired FROM tenants WHERE id = candidate_id FOR SHARE;
  IF retired IS NOT NULL THEN RAISE EXCEPTION 'tenant is retired' USING ERRCODE = '23514'; END IF;
 END LOOP;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
DO $$
DECLARE child TEXT;
BEGIN
 FOREACH child IN ARRAY ARRAY['job_events','job_artifacts','training_checkpoints','training_job_event_tokens','training_job_events','managed_attempt_resources','managed_attempt_fences'] LOOP
  EXECUTE format('CREATE TRIGGER parent_tenant_lifecycle_write_guard BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION require_active_parent_tenant_write(''training_jobs'',''job_id'')', child);
 END LOOP;
END;
$$;
CREATE TRIGGER parent_tenant_lifecycle_write_guard BEFORE INSERT OR UPDATE OR DELETE ON dataset_cache_observations
 FOR EACH ROW EXECUTE FUNCTION require_active_parent_tenant_write('training_jobs','training_job_id');
CREATE TRIGGER parent_tenant_lifecycle_write_guard BEFORE INSERT OR UPDATE OR DELETE ON data_space_upload_parts
 FOR EACH ROW EXECUTE FUNCTION require_active_parent_tenant_write('data_space_uploads','session_id');
