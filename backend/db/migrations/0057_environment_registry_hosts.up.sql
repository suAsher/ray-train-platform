SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Existing operations keep their original Harbor. Defaults also preserve
-- inserts from the previous backend after an application rollback.
ALTER TABLE environment_registry_authorizations
  ADD COLUMN registry_host TEXT NOT NULL DEFAULT 'harbor.wellspiking.ai';
ALTER TABLE environment_builds
  ADD COLUMN registry_host TEXT NOT NULL DEFAULT 'harbor.wellspiking.ai';
