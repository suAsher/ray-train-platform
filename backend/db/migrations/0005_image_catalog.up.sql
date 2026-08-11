-- Image catalog.
--
-- Training and debug images were previously baked into deployment values, so a
-- user could not pick the environment their code needs. An administrator now
-- registers images here and users choose from the catalogue.
CREATE TABLE IF NOT EXISTS platform_images (
  id TEXT PRIMARY KEY,
  -- NULL means the image is visible to every tenant.
  tenant_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  -- Always registry/repo@sha256:<digest>; a mutable tag would silently change
  -- the environment underneath a reproducible training run.
  reference TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('training', 'workspace')),
  description TEXT NOT NULL DEFAULT '',
  framework TEXT NOT NULL DEFAULT '',
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS platform_images_kind_idx ON platform_images(kind);
CREATE INDEX IF NOT EXISTS platform_images_tenant_idx ON platform_images(tenant_id);

-- Git credentials for private repositories.
--
-- Only a reference to a Kubernetes Secret is stored: the token itself never
-- lands in PostgreSQL, and the materializer reads it from the tenant namespace.
CREATE TABLE IF NOT EXISTS git_credentials (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  host TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  secret_name TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS git_credentials_tenant_host_uidx
  ON git_credentials(tenant_id, host);
