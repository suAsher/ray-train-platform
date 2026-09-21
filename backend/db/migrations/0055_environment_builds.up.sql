SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE environment_registry_authorizations (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 username TEXT NOT NULL,
 secret_ref TEXT NOT NULL,
 build_id TEXT NOT NULL DEFAULT '',
 target TEXT NOT NULL DEFAULT '',
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX environment_registry_authorizations_expiry_idx ON environment_registry_authorizations(expires_at);
CREATE INDEX environment_registry_authorizations_owner_idx ON environment_registry_authorizations(tenant_id,owner_id);

CREATE TABLE environment_builds (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL,
 namespace TEXT NOT NULL,
 workspace_resource_name TEXT NOT NULL,
 workspace_uid TEXT NOT NULL,
 base_image TEXT NOT NULL,
 workspace_image TEXT NOT NULL,
 name TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '',
 visibility TEXT NOT NULL CHECK (visibility IN ('personal','team')),
 project TEXT NOT NULL,
 repository TEXT NOT NULL,
 tag TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('QUEUED','CAPTURING','BUILDING','VALIDATING','PUSHING','VERIFYING_PULL','READY','AWAITING_AUTH','FAILED','CANCEL_REQUESTED','CANCELED')),
 resume_status TEXT NOT NULL DEFAULT '',
 message TEXT NOT NULL DEFAULT '',
 auth_id TEXT NOT NULL DEFAULT '',
 idempotency_key TEXT NOT NULL,
 snapshot_json TEXT NOT NULL DEFAULT '',
 artifact_digest TEXT NOT NULL DEFAULT '',
 image_digest TEXT NOT NULL DEFAULT '',
 image_reference TEXT NOT NULL DEFAULT '',
 image_id TEXT NOT NULL DEFAULT '',
 checks_json TEXT NOT NULL DEFAULT '',
 attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt BETWEEN 1 AND 5),
 lease_owner TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,
 artifact_expires_at TIMESTAMPTZ NOT NULL,
 cleaned_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,owner_id,idempotency_key),
 UNIQUE(project,repository,tag)
);
CREATE INDEX environment_builds_reconcile_idx ON environment_builds(status,lease_until,updated_at);
CREATE INDEX environment_builds_owner_idx ON environment_builds(tenant_id,owner_id,created_at);

CREATE TABLE environment_versions (
 id TEXT PRIMARY KEY,
 build_id TEXT NOT NULL UNIQUE REFERENCES environment_builds(id),
 tenant_id TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 visibility TEXT NOT NULL CHECK (visibility IN ('personal','team')),
 name TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '',
 image_id TEXT NOT NULL,
 image_reference TEXT NOT NULL,
 base_image TEXT NOT NULL,
 checks_json TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX environment_versions_visible_idx ON environment_versions(tenant_id,owner_id,visibility);

-- No FK to authorizations: a preallocated ref must survive a failed metadata
-- transaction and be reclaimed even when no authorization row was created.
CREATE TABLE environment_credential_materials (
 ref TEXT PRIMARY KEY,
 authorization_id TEXT NOT NULL,
 tenant_id TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX environment_credential_materials_expiry_idx ON environment_credential_materials(expires_at);
CREATE INDEX environment_credential_materials_auth_idx ON environment_credential_materials(authorization_id);
