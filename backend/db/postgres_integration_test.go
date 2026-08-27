package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresMigrationsIntegration(t *testing.T) {
	database := openPostgresTestSchema(t)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ApplyMigrations(database); err != nil {
			t.Fatalf("ApplyMigrations() attempt %d error = %v", attempt, err)
		}
	}

	var versions []int
	if err := database.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
		t.Fatalf("load migration versions: %v", err)
	}
	if want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22}; !reflectIntSlicesEqual(versions, want) {
		t.Fatalf("migration versions = %v, want %v", versions, want)
	}

	for _, table := range []string{"personal_access_tokens", "source_artifacts"} {
		var count int64
		if err := database.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?", table).Scan(&count).Error; err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", table, count)
		}
	}

	for _, column := range []string{
		"source_artifact_id",
		"submission_origin",
		"external_submission_id",
		"training_engine",
		"ray_version",
		"cluster_attempt",
		"worker_restart_count",
		"resume_checkpoint_id",
		"parent_job_id",
	} {
		var count int64
		if err := database.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'training_jobs' AND column_name = ?", column).Scan(&count).Error; err != nil {
			t.Fatalf("check training_jobs.%s: %v", column, err)
		}
		if count != 1 {
			t.Errorf("training_jobs.%s count = %d, want 1", column, count)
		}
	}

	runtimeColumnDefaults := map[string]string{
		"training_engine":      "'ray-ddp'::text",
		"ray_version":          "'2.35.0'::text",
		"cluster_attempt":      "1",
		"worker_restart_count": "0",
		"resume_checkpoint_id": "''::text",
		"parent_job_id":        "''::text",
	}
	for column, expectedDefault := range runtimeColumnDefaults {
		var metadata struct {
			ColumnName    string
			IsNullable    string
			ColumnDefault string
		}
		if err := database.Raw(`
SELECT column_name, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'training_jobs'
  AND column_name = ?`, column).Scan(&metadata).Error; err != nil {
			t.Fatalf("load training_jobs.%s metadata: %v", column, err)
		}
		if metadata.ColumnName != column {
			t.Errorf("training_jobs.%s metadata not found", column)
			continue
		}
		if metadata.IsNullable != "NO" {
			t.Errorf("training_jobs.%s is_nullable = %q, want NO", column, metadata.IsNullable)
		}
		if metadata.ColumnDefault != expectedDefault {
			t.Errorf("training_jobs.%s default = %q, want %q", column, metadata.ColumnDefault, expectedDefault)
		}
	}

	imageCompatibilityDefaults := map[string]string{
		"ray_version":       "'2.35.0'::text",
		"supported_engines": "'[\"ray-ddp\"]'::jsonb",
	}
	for column, expectedDefault := range imageCompatibilityDefaults {
		var metadata struct {
			ColumnName    string
			IsNullable    string
			ColumnDefault string
		}
		if err := database.Raw(`
SELECT column_name, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'platform_images'
  AND column_name = ?`, column).Scan(&metadata).Error; err != nil {
			t.Fatalf("load platform_images.%s metadata: %v", column, err)
		}
		if metadata.ColumnName != column {
			t.Errorf("platform_images.%s metadata not found", column)
			continue
		}
		if metadata.IsNullable != "NO" {
			t.Errorf("platform_images.%s is_nullable = %q, want NO", column, metadata.IsNullable)
		}
		if metadata.ColumnDefault != expectedDefault {
			t.Errorf("platform_images.%s default = %q, want %q", column, metadata.ColumnDefault, expectedDefault)
		}
	}

	requiredConstraints := []string{
		"personal_access_tokens_user_tenant_fk",
		"personal_access_tokens_scopes_array_check",
		"source_artifacts_user_tenant_fk",
		"source_artifacts_sha256_check",
		"source_artifacts_size_check",
		"source_artifacts_state_check",
		"source_artifacts_tenant_user_sha256_key",
		"training_jobs_training_engine_check",
		"training_jobs_ray_version_check",
		"training_jobs_engine_ray_version_check",
		"training_jobs_cluster_attempt_check",
		"training_jobs_worker_restart_count_check",
		"training_jobs_parent_job_id_check",
		"platform_images_ray_version_check",
		"platform_images_supported_engines_check",
		"platform_images_engine_ray_version_check",
	}
	for _, constraint := range requiredConstraints {
		var count int64
		if err := database.Raw(`
SELECT COUNT(*)
FROM pg_constraint c
JOIN pg_class r ON r.oid = c.conrelid
JOIN pg_namespace n ON n.oid = r.relnamespace
WHERE n.nspname = current_schema() AND c.conname = ?`, constraint).Scan(&count).Error; err != nil {
			t.Fatalf("check constraint %s: %v", constraint, err)
		}
		if count != 1 {
			t.Errorf("constraint %s count = %d, want 1", constraint, count)
		}
	}

	if err := database.Exec(`
INSERT INTO platform_images(id, name, reference, kind)
VALUES ('image-default-compatibility', 'Default compatibility', 'registry.example/runtime:legacy', 'training')`).Error; err != nil {
		t.Fatalf("insert image using compatibility defaults: %v", err)
	}
	var defaultCompatibility struct {
		RayVersion       string
		SupportedEngines string
	}
	if err := database.Raw(`
SELECT ray_version, supported_engines::text AS supported_engines
FROM platform_images WHERE id = 'image-default-compatibility'`).Scan(&defaultCompatibility).Error; err != nil {
		t.Fatalf("read image compatibility defaults: %v", err)
	}
	if defaultCompatibility.RayVersion != "2.35.0" || defaultCompatibility.SupportedEngines != `["ray-ddp"]` {
		t.Errorf("platform image defaults = %+v, want Ray 2.35.0/ray-ddp", defaultCompatibility)
	}

	invalidCompatibilityRows := []struct {
		name             string
		id               string
		rayVersion       string
		supportedEngines string
	}{
		{name: "duplicate ray-ddp", id: "image-duplicate-ddp", rayVersion: "2.56.1", supportedEngines: `["ray-ddp","ray-ddp"]`},
		{name: "duplicate ray-train", id: "image-duplicate-train", rayVersion: "2.56.1", supportedEngines: `["ray-train","ray-train"]`},
		{name: "mixed duplicate", id: "image-mixed-duplicate", rayVersion: "2.56.1", supportedEngines: `["ray-ddp","ray-train","ray-ddp"]`},
		{name: "unknown engine", id: "image-unknown-engine", rayVersion: "2.56.1", supportedEngines: `["unknown"]`},
		{name: "empty engines", id: "image-empty-engines", rayVersion: "2.56.1", supportedEngines: `[]`},
		{name: "legacy ray-train", id: "image-legacy-train", rayVersion: "2.35.0", supportedEngines: `["ray-train"]`},
	}
	for _, test := range invalidCompatibilityRows {
		t.Run("rejects "+test.name, func(t *testing.T) {
			// Keep every constraint probe outside a surrounding transaction. A
			// rejected statement must not poison the following PostgreSQL checks.
			err := database.Exec(`
INSERT INTO platform_images(id, name, reference, kind, ray_version, supported_engines)
VALUES (?, ?, ?, 'training', ?, CAST(? AS JSONB))`,
				test.id, test.name, "registry.example/runtime:"+test.id, test.rayVersion, test.supportedEngines,
			).Error
			if err == nil {
				t.Fatalf("platform image compatibility constraint accepted %s", test.supportedEngines)
			}
		})
	}

	if err := database.Exec(`
INSERT INTO platform_images(id, name, reference, kind, ray_version, supported_engines)
VALUES ('image-valid-mixed-engines', 'Valid mixed engines', 'registry.example/runtime:valid-mixed',
        'training', '2.56.1', '["ray-ddp","ray-train"]'::jsonb)`).Error; err != nil {
		t.Fatalf("valid Ray 2.56.1 mixed-engine image was rejected: %v", err)
	}

	for _, index := range []string{"training_jobs_engine_state_idx", "training_jobs_parent_idx"} {
		var count int64
		if err := database.Raw("SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'training_jobs' AND indexname = ?", index).Scan(&count).Error; err != nil {
			t.Fatalf("check index %s: %v", index, err)
		}
		if count != 1 {
			t.Errorf("index %s count = %d, want 1", index, count)
		}
	}
	var parentIndexDefinition string
	if err := database.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'training_jobs' AND indexname = 'training_jobs_parent_idx'").Scan(&parentIndexDefinition).Error; err != nil {
		t.Fatalf("load training_jobs_parent_idx definition: %v", err)
	}
	if normalized := strings.ToLower(parentIndexDefinition); !strings.Contains(normalized, "where") || !strings.Contains(normalized, "parent_job_id") || !strings.Contains(normalized, "<> ''::text") {
		t.Errorf("training_jobs_parent_idx is not partial: %q", parentIndexDefinition)
	}

	seedPostgresIdentityRows(t, database)
	assertPostgresTenantIsolation(t, database)
}

func TestPostgresAdvisoryLockMutualExclusionAndRelease(t *testing.T) {
	dsn := postgresTestDSN(t)
	first := openIndependentPostgres(t, dsn)
	second := openIndependentPostgres(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := first.Connection(func(firstConnection *gorm.DB) error {
		acquired, err := tryPostgresTestLock(ctx, firstConnection)
		if err != nil {
			return err
		}
		if !acquired {
			return fmt.Errorf("first connection did not acquire migration lock")
		}
		firstHasLock := true
		defer func() {
			if firstHasLock {
				_, _ = releasePostgresTestLock(firstConnection)
			}
		}()

		if err := second.Connection(func(secondConnection *gorm.DB) error {
			acquired, err := tryPostgresTestLock(ctx, secondConnection)
			if err != nil {
				return err
			}
			if acquired {
				_, _ = releasePostgresTestLock(secondConnection)
				return fmt.Errorf("second connection acquired a lock still held by first")
			}

			unlocked, err := releasePostgresTestLock(firstConnection)
			if err != nil {
				return err
			}
			if !unlocked {
				return fmt.Errorf("first connection did not release its lock")
			}
			firstHasLock = false

			acquired, err = tryPostgresTestLock(ctx, secondConnection)
			if err != nil {
				return err
			}
			if !acquired {
				return fmt.Errorf("second connection did not acquire released lock")
			}
			unlocked, err = releasePostgresTestLock(secondConnection)
			if err != nil {
				return err
			}
			if !unlocked {
				return fmt.Errorf("second connection did not release its lock")
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func openPostgresTestSchema(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := postgresTestDSN(t)
	admin := openIndependentPostgres(t, dsn)
	schema := fmt.Sprintf("ray_platform_test_%d", time.Now().UnixNano())
	quotedSchema := quotePostgresIdentifier(schema)
	if err := admin.Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		t.Fatalf("create temporary postgres schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop temporary postgres schema: %v", err)
		}
	})

	database := openIndependentPostgres(t, dsn)
	if err := database.Exec("SET search_path TO " + quotedSchema).Error; err != nil {
		t.Fatalf("set temporary postgres search_path: %v", err)
	}
	return database
}

func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	return dsn
}

func openIndependentPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open postgres test database: %v", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatalf("get postgres test database: %v", err)
	}
	sqlDatabase.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDatabase.Close(); err != nil {
			t.Errorf("close postgres test database: %v", err)
		}
	})
	return database
}

func seedPostgresIdentityRows(t *testing.T, database *gorm.DB) {
	t.Helper()
	if err := database.Exec(`
INSERT INTO tenants(id, name, namespace, local_queue)
VALUES ('tenant-a', 'Tenant A', 'tenant-a', 'tenant-a'),
       ('tenant-b', 'Tenant B', 'tenant-b', 'tenant-b')`).Error; err != nil {
		t.Fatalf("insert tenants: %v", err)
	}
	if err := database.Exec(`
INSERT INTO users(id, oidc_subject, username, tenant_id, roles)
VALUES ('user-a1', 'subject-a1', 'user-a1', 'tenant-a', '[]'::jsonb),
       ('user-a2', 'subject-a2', 'user-a2', 'tenant-a', '[]'::jsonb),
       ('user-b1', 'subject-b1', 'user-b1', 'tenant-b', '[]'::jsonb)`).Error; err != nil {
		t.Fatalf("insert users: %v", err)
	}
}

func assertPostgresTenantIsolation(t *testing.T, database *gorm.DB) {
	t.Helper()
	if err := database.Exec(`
INSERT INTO personal_access_tokens(id, public_id, user_id, tenant_id, token_digest, scopes, expires_at)
VALUES ('pat-mismatch', 'mismatch', 'user-a1', 'tenant-b', 'digest', '[]'::jsonb, NOW() + INTERVAL '1 day')`).Error; err == nil {
		t.Fatal("cross-tenant personal access token insert succeeded")
	}

	sha := strings.Repeat("a", 64)
	if err := database.Exec(`
INSERT INTO source_artifacts(id, tenant_id, user_id, sha256, size_bytes, object_key, upload_expires_at)
VALUES (?, 'tenant-b', 'user-a1', ?, 1, 'mismatch.zip', NOW() + INTERVAL '1 hour')`, "artifact-mismatch", sha).Error; err == nil {
		t.Fatal("cross-tenant source artifact insert succeeded")
	}
	if err := database.Exec(`
INSERT INTO source_artifacts(id, tenant_id, user_id, sha256, size_bytes, object_key, upload_expires_at)
VALUES (?, 'tenant-a', 'user-a1', ?, 1, 'user-a1.zip', NOW() + INTERVAL '1 hour')`, "artifact-a1", sha).Error; err != nil {
		t.Fatalf("insert first user artifact: %v", err)
	}
	if err := database.Exec(`
INSERT INTO source_artifacts(id, tenant_id, user_id, sha256, size_bytes, object_key, upload_expires_at)
VALUES (?, 'tenant-a', 'user-a2', ?, 1, 'user-a2.zip', NOW() + INTERVAL '1 hour')`, "artifact-a2", sha).Error; err != nil {
		t.Fatalf("same digest for another user should be allowed: %v", err)
	}
	if err := database.Exec(`
INSERT INTO source_artifacts(id, tenant_id, user_id, sha256, size_bytes, object_key, upload_expires_at)
VALUES (?, 'tenant-a', 'user-a1', ?, 1, 'duplicate.zip', NOW() + INTERVAL '1 hour')`, "artifact-duplicate", sha).Error; err == nil {
		t.Fatal("duplicate digest for the same tenant/user succeeded")
	}
}

func tryPostgresTestLock(ctx context.Context, database *gorm.DB) (bool, error) {
	var acquired bool
	if err := database.WithContext(ctx).Raw("SELECT pg_try_advisory_lock(?)", migrationLockID).Scan(&acquired).Error; err != nil {
		return false, fmt.Errorf("try advisory lock: %w", err)
	}
	return acquired, nil
}

func releasePostgresTestLock(database *gorm.DB) (bool, error) {
	var unlocked bool
	if err := database.Raw("SELECT pg_advisory_unlock(?)", migrationLockID).Scan(&unlocked).Error; err != nil {
		return false, fmt.Errorf("release advisory lock: %w", err)
	}
	return unlocked, nil
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func reflectIntSlicesEqual(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
