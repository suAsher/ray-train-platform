-- Mutable publisher execution state is deliberately separate from immutable
-- partition plans. A restarted worker can therefore renew a lease or attach
-- a verified receipt without weakening dataset-version snapshot guarantees.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS dataset_publication_partition_attempts (
  dataset_version_id TEXT NOT NULL,
  partition_id TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'PENDING',
  input_fingerprint TEXT NOT NULL,
  plan_sha256 TEXT NOT NULL,
  receipt_sha256 TEXT NOT NULL DEFAULT '',
  attempt BIGINT NOT NULL DEFAULT 0,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (dataset_version_id, partition_id),
  FOREIGN KEY (partition_id, dataset_version_id) REFERENCES dataset_partitions(id, dataset_version_id) ON DELETE CASCADE,
  CONSTRAINT dataset_publication_partition_attempts_state_check CHECK (state IN ('PENDING', 'LEASED', 'COMPLETED', 'FAILED')),
  CONSTRAINT dataset_publication_partition_attempts_input_fingerprint_check CHECK (input_fingerprint ~ '^[0-9a-f]{64}$'),
  CONSTRAINT dataset_publication_partition_attempts_plan_digest_check CHECK (plan_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT dataset_publication_partition_attempts_receipt_digest_check CHECK (receipt_sha256 = '' OR receipt_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT dataset_publication_partition_attempts_attempt_check CHECK (attempt >= 0),
  CONSTRAINT dataset_publication_partition_attempts_lease_check CHECK (
    (state = 'LEASED' AND lease_owner <> '' AND lease_expires_at IS NOT NULL)
    OR (state <> 'LEASED' AND lease_owner = '' AND lease_expires_at IS NULL)
  ),
  CONSTRAINT dataset_publication_partition_attempts_completed_receipt_check CHECK (state <> 'COMPLETED' OR receipt_sha256 <> '')
);

CREATE INDEX IF NOT EXISTS dataset_publication_partition_attempts_dispatch_idx
  ON dataset_publication_partition_attempts(dataset_version_id, state, lease_expires_at, partition_id);
