SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Keep legacy rows readable while allowing operator-provisioned Kubernetes PVCs.
ALTER TABLE storage_assets DROP CONSTRAINT storage_assets_provider_check;
ALTER TABLE storage_assets ADD CONSTRAINT storage_assets_provider_check
  CHECK (provider IN ('tos', 'idc', 'pvc'));
ALTER TABLE storage_assets ADD CONSTRAINT storage_assets_pvc_contract_check
  CHECK (provider <> 'pvc' OR (root_prefix = '' AND browse_enabled = FALSE));
