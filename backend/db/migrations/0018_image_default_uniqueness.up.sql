-- Resolve any legacy duplicate defaults deterministically before enforcing the
-- invariant. The newest catalogue entry wins inside each tenant/kind scope;
-- NULL tenant_id is the independent shared-catalogue scope.
WITH ranked_defaults AS (
  SELECT
    id,
    ROW_NUMBER() OVER (
      PARTITION BY tenant_id, kind
      ORDER BY updated_at DESC, created_at DESC, id DESC
    ) AS position
  FROM platform_images
  WHERE is_default = TRUE
)
UPDATE platform_images
SET is_default = FALSE,
    updated_at = NOW()
WHERE id IN (
  SELECT id FROM ranked_defaults WHERE position > 1
);

-- PostgreSQL treats NULL values as distinct in a normal unique index, so the
-- shared and tenant-scoped defaults require separate partial indexes.
CREATE UNIQUE INDEX IF NOT EXISTS platform_images_tenant_default_uidx
  ON platform_images(tenant_id, kind)
  WHERE tenant_id IS NOT NULL AND is_default = TRUE;

CREATE UNIQUE INDEX IF NOT EXISTS platform_images_shared_default_uidx
  ON platform_images(kind)
  WHERE tenant_id IS NULL AND is_default = TRUE;
