SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Only local account decommissioning is admitted after retirement. There is no
-- session-variable bypass and no exception for other credential/data tables.
CREATE FUNCTION require_active_local_user_or_decommission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_tenant_id TEXT; new_tenant_id TEXT; candidate_id TEXT; retired TIMESTAMPTZ;
BEGIN
 IF TG_OP <> 'INSERT' THEN old_tenant_id := OLD.tenant_id; END IF;
 IF TG_OP <> 'DELETE' THEN new_tenant_id := NEW.tenant_id; END IF;
 FOR candidate_id IN
  SELECT DISTINCT tenant_id FROM unnest(ARRAY[old_tenant_id,new_tenant_id]) AS candidates(tenant_id)
  WHERE tenant_id IS NOT NULL AND tenant_id <> '' ORDER BY tenant_id
 LOOP
  PERFORM pg_advisory_xact_lock_shared(hashtextextended(candidate_id, 34781));
  SELECT retired_at INTO retired FROM tenants WHERE id = candidate_id FOR SHARE;
  IF retired IS NOT NULL THEN
   IF TG_OP = 'UPDATE' AND NEW.disabled IS TRUE
    AND OLD.decommissioned_at IS NULL AND NEW.decommissioned_at IS NOT NULL
    AND (to_jsonb(NEW) - ARRAY['disabled','decommissioned_at','updated_at'])
      = (to_jsonb(OLD) - ARRAY['disabled','decommissioned_at','updated_at'])
   THEN RETURN NEW;
   END IF;
   RAISE EXCEPTION 'tenant is retired' USING ERRCODE = '23514';
  END IF;
 END LOOP;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER tenant_lifecycle_write_guard ON local_users;
CREATE TRIGGER tenant_lifecycle_write_guard BEFORE INSERT OR UPDATE OR DELETE ON local_users
 FOR EACH ROW EXECUTE FUNCTION require_active_local_user_or_decommission();
