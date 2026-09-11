SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE tenants
  ADD COLUMN IF NOT EXISTS accelerator_class TEXT NOT NULL DEFAULT 'rtx4090';

ALTER TABLE tenants
  DROP CONSTRAINT IF EXISTS tenants_accelerator_class_check;

ALTER TABLE tenants
  ADD CONSTRAINT tenants_accelerator_class_check
  CHECK (accelerator_class IN ('rtx4090', 'a100', 'a800', 'h20'));
