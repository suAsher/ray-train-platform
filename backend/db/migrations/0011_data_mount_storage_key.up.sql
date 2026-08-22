-- Keep workload ownership on the opaque platform user id, but record a
-- separate stable storage key for the physical TOS root. Existing bindings
-- deliberately retain their current subject-based root until an explicit
-- data-copy migration verifies the new username root.
ALTER TABLE data_mount_bindings
  ADD COLUMN IF NOT EXISTS storage_key TEXT NOT NULL DEFAULT '';

UPDATE data_mount_bindings
  SET storage_key = user_id
  WHERE scope = 'personal'
    AND storage_key = ''
    AND user_id IS NOT NULL;
