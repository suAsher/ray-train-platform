package db

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/config"
)

func TestOpenRejectsMissingDatabaseURL(t *testing.T) {
	_, err := Open(config.Config{})
	if err == nil {
		t.Fatal("expected missing database URL error")
	}
	if err.Error() != "DATABASE_URL is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMigrationVersionsEmbedded(t *testing.T) {
	versions, err := migrationVersions(migrationFiles)
	if err != nil {
		t.Fatalf("migrationVersions() error = %v", err)
	}
	if want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("migrationVersions() = %v, want %v", versions, want)
	}
}

func TestImageRuntimeCompatibilityMigrationEnforcesUniqueSupportedEngines(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0021_image_runtime_compatibility.up.sql")
	if err != nil {
		t.Fatalf("read image runtime compatibility migration: %v", err)
	}

	sql := strings.Join(strings.Fields(string(contents)), " ")
	uniqueEngineCount := strings.Join(strings.Fields(`
jsonb_array_length(supported_engines) =
  (CASE WHEN supported_engines @> '["ray-ddp"]'::jsonb THEN 1 ELSE 0 END) +
  (CASE WHEN supported_engines @> '["ray-train"]'::jsonb THEN 1 ELSE 0 END)
`), " ")
	if !strings.Contains(sql, uniqueEngineCount) {
		t.Fatalf("migration 21 does not enforce unique supported engines; missing %q", uniqueEngineCount)
	}
}

func TestTrainingCheckpointMigrationEnforcesManagedEventContracts(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0022_training_checkpoints.up.sql")
	if err != nil {
		t.Fatalf("read training checkpoint migration: %v", err)
	}

	sql := strings.Join(strings.Fields(string(contents)), " ")
	required := []string{
		"PRIMARY KEY (job_id, id)",
		"token_sha256 ~ '^[0-9a-f]{64}$'",
		"event_type IN ('WORKER_GROUP_STARTED', 'CHECKPOINT_COMPLETE', 'TRAINING_PROGRESS')",
		"last_generation BETWEEN 0 AND 1000000000000",
		"last_epoch BETWEEN 0 AND 1000000000000",
		"last_step BETWEEN 0 AND 1000000000000",
		"rate_count BETWEEN 0 AND 120",
		"generation BIGINT NOT NULL DEFAULT 1",
		"generation BETWEEN 1 AND 1000000000000",
		"epoch BETWEEN 0 AND 1000000000000",
		"step BETWEEN 0 AND 1000000000000",
		"complete = FALSE OR manifest_sha256 ~ '^[0-9a-f]{64}$'",
		"WHERE complete = TRUE",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 22 missing %q", fragment)
		}
	}
	if strings.Contains(string(contents), "\n  id TEXT PRIMARY KEY,") {
		t.Error("migration 22 scopes checkpoint identity globally instead of by job")
	}
	if strings.Count(sql, "event_type IN (") != 1 {
		t.Error("migration 22 event allowlist is missing or ambiguous")
	}
}

func TestSubmissionGatewayMigrationEnforcesIsolationAndBounds(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0002_submission_gateway.up.sql")
	if err != nil {
		t.Fatalf("read migration 2: %v", err)
	}
	sql := string(contents)
	required := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS users_id_tenant_uidx",
		"ON users(id, tenant_id)",
		"UNIQUE (tenant_id, user_id, sha256)",
		"CHECK (jsonb_typeof(scopes) = 'array')",
		"CHECK (sha256 ~ '^[0-9a-f]{64}$')",
		"CHECK (state IN ('PENDING', 'READY', 'FAILED', 'EXPIRED'))",
		"CHECK (size_bytes BETWEEN 1 AND 2147483648)",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 2 missing %q", fragment)
		}
	}
	if count := strings.Count(sql, "FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id)"); count != 2 {
		t.Errorf("composite user/tenant foreign key count = %d, want 2", count)
	}
	if strings.Contains(sql, "user_id TEXT NOT NULL REFERENCES users(id)") {
		t.Error("migration 2 permits an independent user reference without tenant binding")
	}
}

func TestMigrationVersionsFilenameHandling(t *testing.T) {
	t.Run("empty filesystem", func(t *testing.T) {
		versions, err := migrationVersions(fstest.MapFS{})
		if err != nil {
			t.Fatalf("migrationVersions() error = %v", err)
		}
		if len(versions) != 0 {
			t.Fatalf("migrationVersions() = %v, want empty", versions)
		}
	})

	t.Run("ignores non migration files", func(t *testing.T) {
		files := fstest.MapFS{
			"migrations/README.md": &fstest.MapFile{Data: []byte("notes")},
		}
		versions, err := migrationVersions(files)
		if err != nil {
			t.Fatalf("migrationVersions() error = %v", err)
		}
		if len(versions) != 0 {
			t.Fatalf("migrationVersions() = %v, want empty", versions)
		}
	})

	t.Run("rejects malformed up migration", func(t *testing.T) {
		files := fstest.MapFS{
			"migrations/not-a-version.up.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
		}
		if _, err := migrationVersions(files); err == nil {
			t.Fatal("migrationVersions() error = nil, want malformed filename error")
		}
	})

	t.Run("rejects duplicate version", func(t *testing.T) {
		files := fstest.MapFS{
			"migrations/0001_first.up.sql":  &fstest.MapFile{Data: []byte("SELECT 1")},
			"migrations/0001_second.up.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
		}
		if _, err := migrationVersions(files); err == nil {
			t.Fatal("migrationVersions() error = nil, want duplicate version error")
		}
	})
}

func TestApplyMigrationsWithFSEmpty(t *testing.T) {
	database := openSQLite(t)
	if err := applyMigrations(database, fstest.MapFS{}); err != nil {
		t.Fatalf("applyMigrations() error = %v", err)
	}

	var count int64
	if err := database.Raw("SELECT COUNT(*) FROM schema_migrations").Scan(&count).Error; err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("schema migration count = %d, want 0", count)
	}
}

func TestApplyMigrationsWithFSIsIdempotent(t *testing.T) {
	database := openSQLite(t)
	files := fstest.MapFS{
		"migrations/0002_second.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE second_table (id INTEGER PRIMARY KEY);")},
		"migrations/0001_first.up.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first_table (id INTEGER PRIMARY KEY);")},
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := applyMigrations(database, files); err != nil {
			t.Fatalf("applyMigrations() attempt %d error = %v", attempt, err)
		}
	}

	var versions []int
	if err := database.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
		t.Fatalf("load schema migrations: %v", err)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("applied versions = %v, want %v", versions, want)
	}
}

func TestApplyMigrationsWithFSRollsBackFailedMigration(t *testing.T) {
	database := openSQLite(t)
	files := fstest.MapFS{
		"migrations/0001_first.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE first_table (id INTEGER PRIMARY KEY);")},
		"migrations/0002_broken.up.sql": &fstest.MapFile{Data: []byte(`
CREATE TABLE rolled_back_table (id INTEGER PRIMARY KEY);
INSERT INTO missing_table(id) VALUES (1);
`)},
	}

	if err := applyMigrations(database, files); err == nil {
		t.Fatal("applyMigrations() error = nil, want migration failure")
	}

	var versions []int
	if err := database.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
		t.Fatalf("load schema migrations: %v", err)
	}
	if want := []int{1}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("applied versions = %v, want %v", versions, want)
	}

	var tableCount int64
	if err := database.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rolled_back_table'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("check rolled back table: %v", err)
	}
	if tableCount != 0 {
		t.Fatal("rolled_back_table exists after failed migration")
	}
}

func TestAcquireMigrationLockRetriesUntilSuccess(t *testing.T) {
	attempts := 0
	err := acquireMigrationLock(context.Background(), time.Nanosecond, func(context.Context) (bool, error) {
		attempts++
		return attempts == 3, nil
	})
	if err != nil {
		t.Fatalf("acquireMigrationLock() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestAcquireMigrationLockTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	attempts := 0
	err := acquireMigrationLock(ctx, time.Millisecond, func(context.Context) (bool, error) {
		attempts++
		return false, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireMigrationLock() error = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("acquireMigrationLock() error = %q, want explicit timeout", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2", attempts)
	}
}

func TestWithMigrationLockCallsInOrder(t *testing.T) {
	calls := make([]string, 0, 3)
	err := withMigrationLock(
		func() error {
			calls = append(calls, "lock")
			return nil
		},
		func() error {
			calls = append(calls, "migrate")
			return nil
		},
		func() (bool, error) {
			calls = append(calls, "unlock")
			return true, nil
		},
	)
	if err != nil {
		t.Fatalf("withMigrationLock() error = %v", err)
	}
	if want := []string{"lock", "migrate", "unlock"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestWithMigrationLockRejectsUnlockFalse(t *testing.T) {
	err := withMigrationLock(
		func() error { return nil },
		func() error { return nil },
		func() (bool, error) { return false, nil },
	)
	if err == nil {
		t.Fatal("withMigrationLock() error = nil, want unlock ownership error")
	}
	if !strings.Contains(err.Error(), "release migration lock") || !strings.Contains(err.Error(), "not held by this session") {
		t.Fatalf("withMigrationLock() error = %q, want release/ownership context", err)
	}
}

func TestWithMigrationLockPreservesMigrationErrorWhenUnlockFails(t *testing.T) {
	migrationErr := errors.New("migration failed")
	unlockQueryErr := errors.New("connection lost")
	tests := []struct {
		name             string
		unlock           func() (bool, error)
		wantReleaseError error
	}{
		{name: "lock not owned", unlock: func() (bool, error) { return false, nil }},
		{name: "unlock query error", unlock: func() (bool, error) { return false, unlockQueryErr }, wantReleaseError: unlockQueryErr},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := withMigrationLock(
				func() error { return nil },
				func() error { return migrationErr },
				test.unlock,
			)
			if !errors.Is(err, migrationErr) {
				t.Fatalf("withMigrationLock() error = %v, want migration error preserved", err)
			}
			if test.wantReleaseError != nil && !errors.Is(err, test.wantReleaseError) {
				t.Fatalf("withMigrationLock() error = %v, want release error preserved", err)
			}
			if !strings.Contains(err.Error(), "release migration lock") {
				t.Fatalf("withMigrationLock() error = %q, want unlock context", err)
			}
		})
	}
}

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatalf("get sqlite database: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDatabase.Close(); err != nil {
			t.Errorf("close sqlite database: %v", err)
		}
	})
	return database
}
