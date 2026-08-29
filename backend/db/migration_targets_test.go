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
