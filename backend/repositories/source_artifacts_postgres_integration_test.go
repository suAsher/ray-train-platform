package repositories

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
	"ray-train-platform-backend/auth"
	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
)

func TestSourceArtifactRepositoryPostgresIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlAdmin, err := admin.DB()
	if err != nil {
		t.Fatalf("get postgres connection: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlAdmin.Close(); err != nil {
			t.Errorf("close postgres connection: %v", err)
		}
	})
	schema := fmt.Sprintf("artifact_repo_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if err := admin.Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	if err := admin.Exec("SET search_path TO " + quotedSchema).Error; err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if err := databasepkg.ApplyMigrations(admin); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	repo := NewGormRepository(admin)
	principal := auth.Principal{Subject: "pg-user", TenantID: "pg-tenant", Username: "pg-user", Roles: []string{"Engineer"}}
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	expires := time.Now().UTC().Truncate(time.Microsecond).Add(15 * time.Minute)
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: "pg-artifact", TenantID: principal.TenantID, UserID: principal.Subject,
		SHA256: repositoryArtifactDigest, SizeBytes: 100,
	}, expires, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.CreateOrReuseSourceArtifact(context.Background(), &artifact)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	artifact.ID = "pg-artifact-retry"
	artifact.UploadExpiresAt = expires.Add(time.Minute)
	reused, err := repo.CreateOrReuseSourceArtifact(context.Background(), &artifact)
	if err != nil {
		t.Fatalf("reuse artifact: %v", err)
	}
	if reused.ID != created.ID || reused.UploadExpiresAt != artifact.UploadExpiresAt {
		t.Fatalf("postgres reuse mismatch: created=%q reused=%q expiry=%s", created.ID, reused.ID, reused.UploadExpiresAt)
	}
	ready, err := repo.MarkSourceArtifactReady(context.Background(), principal.TenantID, principal.Subject, created.ID, time.Now().UTC())
	if err != nil || ready.State != domain.SourceArtifactReady {
		t.Fatalf("complete artifact: state=%q err=%v", ready.State, err)
	}
}
