-- Source archive ownership remains an opaque user id, while the physical
-- archive location follows the persisted personal data root. Existing rows
-- deliberately stay on their legacy subject-derived root until a separately
-- verified object-copy migration is approved.
ALTER TABLE source_artifacts
  ADD COLUMN IF NOT EXISTS storage_root TEXT NOT NULL DEFAULT '';

UPDATE source_artifacts
  SET storage_root = 'ray-train/tenants/' || tenant_id || '/users/' || user_id || '/'
  WHERE storage_root = '';
