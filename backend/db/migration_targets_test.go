package db

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	createTablePattern = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	alterTablePattern  = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	createIndexPattern = regexp.MustCompile(`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?[A-Za-z0-9_]+\s+ON\s+(?:ONLY\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
)

// A migration naming a table that does not exist only fails at startup, in the
// target environment, after the image is built and rolled out: the backend
// crash-loops on ApplyMigrations. This catches the mistake at test time
// without needing a live PostgreSQL.
//
// It caught exactly that: 0014 was written against `jobs`, while the table is
// `training_jobs`.
func TestMigrationsOnlyTouchTablesEarlierMigrationsCreate(t *testing.T) {
	versions, err := migrationVersions(migrationFiles)
	if err != nil {
		t.Fatalf("migrationVersions() error = %v", err)
	}
	names, err := migrationFileNames()
	if err != nil {
		t.Fatalf("read migration names: %v", err)
	}
	if len(names) != len(versions) {
		t.Fatalf("expected one file per version, got %d files for %d versions", len(names), len(versions))
	}

	known := map[string]bool{}
	for _, name := range names {
		contents, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		statements := stripSQLComments(string(contents))
		for _, match := range createTablePattern.FindAllStringSubmatch(statements, -1) {
			known[strings.ToLower(match[1])] = true
		}
		for _, pattern := range []*regexp.Regexp{alterTablePattern, createIndexPattern} {
			for _, match := range pattern.FindAllStringSubmatch(statements, -1) {
				table := strings.ToLower(match[1])
				if !known[table] {
					t.Errorf("%s references table %q, which no migration up to and including this one creates (known: %s)",
						name, table, sortedKeys(known))
				}
			}
		}
	}
}

func TestTrainingRuntimeMetadataMigrationTargetsTrainingJobs(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0020_training_runtime_metadata.up.sql")
	if err != nil {
		t.Fatalf("read training runtime metadata migration: %v", err)
	}

	sql := string(contents)
	for _, fragment := range []string{
		"SET LOCAL lock_timeout = '5s';",
		"SET LOCAL statement_timeout = '60s';",
		"ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS training_engine TEXT NOT NULL DEFAULT 'ray-ddp';",
		"ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS ray_version TEXT NOT NULL DEFAULT '2.35.0';",
		"ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS cluster_attempt INTEGER NOT NULL DEFAULT 1;",
		"ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS worker_restart_count INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS resume_checkpoint_id TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE training_jobs ADD COLUMN IF NOT EXISTS parent_job_id TEXT NOT NULL DEFAULT '';",
		"CONSTRAINT training_jobs_training_engine_check CHECK (training_engine IN ('ray-ddp', 'ray-train'))",
		"CONSTRAINT training_jobs_ray_version_check CHECK (ray_version IN ('2.35.0', '2.56.1', '2.58.0'))",
		"CONSTRAINT training_jobs_engine_ray_version_check CHECK (training_engine <> 'ray-train' OR ray_version <> '2.35.0')",
		"CONSTRAINT training_jobs_cluster_attempt_check CHECK (cluster_attempt >= 1)",
		"CONSTRAINT training_jobs_worker_restart_count_check CHECK (worker_restart_count >= 0)",
		"CONSTRAINT training_jobs_parent_job_id_check CHECK (parent_job_id = '' OR parent_job_id ~ '^job-[0-9a-f]{24}$')",
		"ON training_jobs(training_engine, observed_state)",
		"ON training_jobs(parent_job_id) WHERE parent_job_id <> ''",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("training runtime metadata migration missing %q", fragment)
		}
	}
}

func TestDatasetVersioningMigrationDefinesAdditiveSchema(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0024_dataset_versioning.up.sql")
	if err != nil {
		t.Fatalf("read dataset versioning migration: %v", err)
	}

	sql := string(contents)
	tables := map[string]bool{}
	for _, match := range createTablePattern.FindAllStringSubmatch(stripSQLComments(sql), -1) {
		tables[strings.ToLower(match[1])] = true
	}
	for _, table := range []string{
		"datasets",
		"dataset_versions",
		"dataset_partitions",
		"dataset_publication_runs",
		"dataset_version_shards",
		"dataset_cache_observations",
	} {
		if !tables[table] {
			t.Errorf("dataset versioning migration does not create %s", table)
		}
	}

	for _, fragment := range []string{
		"SET LOCAL lock_timeout = '5s';",
		"SET LOCAL statement_timeout = '60s';",
		"owner_tenant_id TEXT REFERENCES tenants(id) ON DELETE RESTRICT",
		"CONSTRAINT datasets_visibility_check CHECK (visibility IN ('PUBLIC', 'TEAM'))",
		"CONSTRAINT datasets_visibility_owner_check CHECK (",
		"visibility = 'PUBLIC' AND owner_tenant_id IS NULL AND source_space = 'public'",
		"visibility = 'TEAM' AND owner_tenant_id IS NOT NULL AND source_space = 'team-shared'",
		"CONSTRAINT dataset_versions_state_check CHECK (state IN ('DISCOVERING', 'STABILIZING', 'VALIDATING', 'PACKING', 'READY', 'FAILED', 'DEPRECATED', 'RETIRED'))",
		"CONSTRAINT dataset_versions_counts_check CHECK (",
		"CONSTRAINT dataset_versions_manifest_digest_check CHECK (manifest_sha256 IS NULL OR manifest_sha256 ~ '^[0-9a-f]{64}$')",
		"CONSTRAINT dataset_versions_ready_manifest_check CHECK (",
		"UNIQUE (dataset_id, version)",
		"UNIQUE (id, dataset_id)",
		"CONSTRAINT dataset_partitions_progress_check CHECK (",
		"UNIQUE (dataset_version_id, name)",
		"CONSTRAINT dataset_publication_runs_state_check CHECK (state IN ('DISCOVERING', 'STABILIZING', 'VALIDATING', 'PACKING', 'READY', 'FAILED', 'DEPRECATED', 'RETIRED'))",
		"CONSTRAINT dataset_publication_runs_partition_progress_check CHECK (",
		"CONSTRAINT dataset_publication_runs_object_progress_check CHECK (",
		"FOREIGN KEY (dataset_version_id, dataset_id) REFERENCES dataset_versions(id, dataset_id) ON DELETE CASCADE",
		"CONSTRAINT dataset_version_shards_digest_check CHECK (shard_sha256 ~ '^[0-9a-f]{64}$')",
		"CONSTRAINT dataset_version_shards_split_check CHECK (split IN ('train', 'val', 'test'))",
		"CONSTRAINT dataset_version_shards_counts_check CHECK (",
		"FOREIGN KEY (partition_id, dataset_version_id) REFERENCES dataset_partitions(id, dataset_version_id) ON DELETE CASCADE",
		"CONSTRAINT dataset_cache_observations_counters_check CHECK (",
		"training_job_id TEXT NOT NULL",
		"CONSTRAINT training_jobs_dataset_manifest_digest_check",
		"CONSTRAINT training_jobs_dataset_data_mode_check",
		"dataset_data_mode = 'streaming'",
		"CONSTRAINT training_jobs_dataset_cache_policy_check",
		"dataset_cache_policy IN ('auto', 'off', 'bounded')",
		"CONSTRAINT training_jobs_dataset_provenance_check",
		"FOREIGN KEY (dataset_version_id, dataset_id) REFERENCES dataset_versions(id, dataset_id) ON DELETE RESTRICT",
		"CREATE OR REPLACE FUNCTION enforce_training_job_dataset_scope()",
		"CREATE TRIGGER training_jobs_dataset_scope_guard",
		"CREATE OR REPLACE FUNCTION enforce_dataset_version_immutability()",
		"CREATE TRIGGER dataset_versions_immutability_guard",
		"CREATE OR REPLACE FUNCTION enforce_dataset_version_child_immutability()",
		"CREATE TRIGGER dataset_partitions_immutability_guard",
		"CREATE TRIGGER dataset_version_shards_immutability_guard",
		"CONSTRAINT dataset_version_shards_dataset_fk",
		"FOREIGN KEY (dataset_version_id, dataset_id) REFERENCES dataset_versions(id, dataset_id) ON DELETE CASCADE",
		"CONSTRAINT dataset_cache_observations_job_version_fk",
		"FOREIGN KEY (training_job_id, dataset_version_id) REFERENCES training_jobs(id, dataset_version_id) ON DELETE CASCADE",
		"manifest_object_key = 'ray-train/platform/datasets/' || dataset_id || '/manifests/' || id || '.parquet'",
		"object_key = 'ray-train/platform/datasets/' || dataset_id || '/objects/sha256/' || left(shard_sha256, 2) || '/' || shard_sha256 || '.parquet'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("dataset versioning migration missing %q", fragment)
		}
	}

	for _, index := range []string{
		"datasets_visibility_owner_idx",
		"datasets_public_slug_uidx",
		"datasets_team_slug_uidx",
		"dataset_versions_ready_latest_idx",
		"dataset_versions_state_idx",
		"dataset_partitions_version_idx",
		"dataset_publication_runs_dataset_state_idx",
		"dataset_publication_runs_version_state_idx",
		"dataset_version_shards_version_split_idx",
		"dataset_version_shards_digest_idx",
		"dataset_cache_observations_job_idx",
		"dataset_cache_observations_version_idx",
		"training_jobs_dataset_version_idx",
		"training_jobs_id_dataset_version_uidx",
	} {
		if !regexp.MustCompile(`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+` + regexp.QuoteMeta(index) + `\b`).MatchString(sql) {
			t.Errorf("dataset versioning migration missing index %s", index)
		}
	}

	if strings.Contains(sql, "slug TEXT NOT NULL UNIQUE") {
		t.Error("team dataset slugs must not be globally unique")
	}
}

func TestDatasetVersioningTrainingJobProvenanceIsNullableWithoutBackfill(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0024_dataset_versioning.up.sql")
	if err != nil {
		t.Fatalf("read dataset versioning migration: %v", err)
	}

	sql := stripSQLComments(string(contents))
	for _, column := range []string{
		"dataset_id",
		"dataset_version_id",
		"dataset_manifest_digest",
		"dataset_data_mode",
		"dataset_cache_policy",
	} {
		pattern := regexp.MustCompile(`(?im)ALTER\s+TABLE\s+training_jobs\s+ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\s+` + regexp.QuoteMeta(column) + `\s+TEXT\s*;`)
		if !pattern.MatchString(sql) {
			t.Errorf("training_jobs provenance column %s must be added as nullable TEXT", column)
		}
	}

	if regexp.MustCompile(`(?i)\b(?:UPDATE|INSERT\s+INTO)\s+training_jobs\b`).MatchString(sql) {
		t.Error("dataset versioning migration must not backfill training_jobs")
	}
	if strings.Contains(strings.ToLower(sql), "dataset_manifest_object_key") {
		t.Error("training_jobs must not store an internal dataset manifest object key")
	}
}

func migrationFileNames() ([]string, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// stripSQLComments removes `--` line comments so prose describing a former
// table name is not mistaken for a statement.
func stripSQLComments(contents string) string {
	lines := strings.Split(contents, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func sortedKeys(values map[string]bool) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}
