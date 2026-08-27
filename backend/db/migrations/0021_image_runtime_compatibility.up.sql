-- platform_images is a compact control-plane table; fail and roll back instead
-- of waiting indefinitely for a schema lock.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE platform_images ADD COLUMN IF NOT EXISTS ray_version TEXT NOT NULL DEFAULT '2.35.0';
ALTER TABLE platform_images ADD COLUMN IF NOT EXISTS supported_engines JSONB NOT NULL DEFAULT '["ray-ddp"]'::jsonb;

ALTER TABLE platform_images
  ADD CONSTRAINT platform_images_ray_version_check
    CHECK (ray_version IN ('2.35.0', '2.56.1', '2.58.0')),
  ADD CONSTRAINT platform_images_supported_engines_check
    CHECK (
      jsonb_typeof(supported_engines) = 'array'
      AND jsonb_array_length(supported_engines) > 0
      AND supported_engines <@ '["ray-ddp", "ray-train"]'::jsonb
    ),
  ADD CONSTRAINT platform_images_engine_ray_version_check
    CHECK (ray_version <> '2.35.0' OR NOT supported_engines @> '["ray-train"]'::jsonb);
