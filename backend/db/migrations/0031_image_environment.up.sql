SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Documentation supplied by administrators, not a runtime verification result.
ALTER TABLE platform_images
    ADD COLUMN environment JSONB NOT NULL DEFAULT '{}';
