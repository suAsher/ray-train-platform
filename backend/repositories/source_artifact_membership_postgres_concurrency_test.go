package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
)

type sourceMembershipConcurrentResult struct {
	artifact *domain.SourceArtifact
	err      error
}

func TestSourceArtifactMembershipPostgresConcurrentQuota(t *testing.T) {
	for _, operation := range []string{"legacy", "request", "reopen"} {
		t.Run(operation, func(t *testing.T) {
			repos, observer := sourceMembershipPostgresConnections(t, 3)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			limits := SourceArtifactLimits{MaxPending: 1, QuotaBytes: 1024}
			inputs := make([]domain.SourceArtifact, 2)
			pids := make([]int, 2)
			for index := range inputs {
				inputs[index] = sourceMembershipConcurrentArtifact(t, fmt.Sprintf("concurrent-%d", index), operation, index+1)
				if operation == "reopen" {
					ready, err := inputs[index].MarkReady(time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
					if err := repos[0].db.Create(sourceArtifactRecordPointer(ready)).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := repos[index].db.Raw("SELECT pg_backend_pid()").Scan(&pids[index]).Error; err != nil {
					t.Fatal(err)
				}
			}
			blocker := repos[2].db.WithContext(ctx).Begin()
			if blocker.Error != nil {
				t.Fatal(blocker.Error)
			}
			defer blocker.Rollback()
			var owner UserRecord
			if err := blocker.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", "user-a").First(&owner).Error; err != nil {
				t.Fatal(err)
			}
			results := make(chan sourceMembershipConcurrentResult, 2)
			for index, input := range inputs {
				go func(repo *GormRepository, artifact domain.SourceArtifact, requestIndex int) {
					stored, err := performSourceMembershipConcurrentOperation(ctx, repo, operation, artifact, requestIndex, limits)
					results <- sourceMembershipConcurrentResult{artifact: stored, err: err}
				}(repos[index], input, index+1)
			}
			for _, pid := range pids {
				waitSourceMembershipPostgresLock(t, ctx, observer, pid, "users", results)
			}
			if err := blocker.Commit().Error; err != nil {
				t.Fatal(err)
			}
			succeeded, denied := 0, 0
			for index := 0; index < 2; index++ {
				select {
				case result := <-results:
					switch {
					case result.err == nil && result.artifact != nil:
						succeeded++
						if result.artifact.TenantID != "team-b" || result.artifact.StorageRoot != inputs[0].StorageRoot {
							t.Fatalf("concurrent upload changed ownership: %+v", result.artifact)
						}
					case errors.Is(result.err, ErrSourceArtifactQuotaExceeded):
						denied++
					default:
						t.Fatalf("unexpected concurrent %s result: artifact=%v err=%v", operation, result.artifact, result.err)
					}
				case <-ctx.Done():
					t.Fatalf("concurrent upload did not finish: %v", ctx.Err())
				}
			}
			if succeeded != 1 || denied != 1 {
				t.Fatalf("single pending quota admitted %d uploads and rejected %d", succeeded, denied)
			}
			var pending int64
			if err := repos[0].db.Model(&SourceArtifactRecord{}).Where("tenant_id = ? AND user_id = ? AND state = ?", "team-b", "user-a", domain.SourceArtifactPending).Count(&pending).Error; err != nil || pending != 1 {
				t.Fatalf("pending artifacts=%d err=%v", pending, err)
			}
		})
	}
}

func TestSourceArtifactMembershipPostgresRechecksAfterConcurrentReassignment(t *testing.T) {
	for _, operation := range []string{"legacy", "request", "reopen"} {
		t.Run(operation, func(t *testing.T) {
			repos, observer := sourceMembershipPostgresConnections(t, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			input := sourceMembershipConcurrentArtifact(t, "reassigned-upload", operation, 1)
			if operation == "reopen" {
				ready, err := input.MarkReady(time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if err := repos[0].db.Create(sourceArtifactRecordPointer(ready)).Error; err != nil {
					t.Fatal(err)
				}
			}
			var pid int
			if err := repos[1].db.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				t.Fatal(err)
			}
			migration := repos[0].db.WithContext(ctx).Begin()
			if migration.Error != nil {
				t.Fatal(migration.Error)
			}
			defer migration.Rollback()
			// Keep the actual reassignment's local user and membership row locks
			// until the waiting upload has reached its authorization query.
			if err := NewGormRepository(migration).ReassignActiveMembership(ctx, "user-a", "team-b", "team-a", []string{domain.RoleEngineer}, true); err != nil {
				t.Fatal(err)
			}
			results := make(chan sourceMembershipConcurrentResult, 1)
			go func() {
				stored, err := performSourceMembershipConcurrentOperation(ctx, repos[1], operation, input, 1, DefaultSourceArtifactLimits())
				results <- sourceMembershipConcurrentResult{artifact: stored, err: err}
			}()
			waitSourceMembershipPostgresLock(t, ctx, observer, pid, "local_users", results)
			if err := migration.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-results:
				if !errors.Is(result.err, ErrSourceArtifactNotFound) || result.artifact != nil {
					t.Fatalf("upload crossed inactive membership after reassignment: artifact=%v err=%v", result.artifact, result.err)
				}
			case <-ctx.Done():
				t.Fatalf("upload did not resume after reassignment: %v", ctx.Err())
			}
			var pending int64
			if err := repos[0].db.Model(&SourceArtifactRecord{}).Where("tenant_id = ? AND user_id = ? AND state = ?", "team-b", "user-a", domain.SourceArtifactPending).Count(&pending).Error; err != nil || pending != 0 {
				t.Fatalf("unauthorized pending artifacts=%d err=%v", pending, err)
			}
		})
	}
}

func sourceMembershipPostgresConnections(t *testing.T, count int) ([]*GormRepository, *gorm.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	observer := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("artifact_membership_concurrency_%d", time.Now().UnixNano())
	if err := observer.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	repos := make([]*GormRepository, count)
	for index := range repos {
		database := openArtifactPostgresConnection(t, dsn)
		if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
			t.Fatal(err)
		}
		repos[index] = NewGormRepository(database)
	}
	if err := databasepkg.ApplyMigrations(repos[0].db); err != nil {
		t.Fatal(err)
	}
	seedSourceArtifactMembershipIdentity(t, repos[0])
	if err := repos[0].ReassignActiveMembership(context.Background(), "user-a", "team-a", "team-b", []string{domain.RoleEngineer}, true); err != nil {
		t.Fatal(err)
	}
	return repos, observer
}

func sourceMembershipConcurrentArtifact(t *testing.T, id, operation string, digestDigit int) domain.SourceArtifact {
	t.Helper()
	artifact := sourceMembershipArtifact(t, id, "team-b", operation == "request")
	artifact.SHA256 = strings.Repeat(fmt.Sprintf("%x", digestDigit), 64)
	var err error
	if operation == "request" {
		artifact.ObjectKey, err = domain.SourceArtifactRequestObjectKeyForRoot(artifact.TenantID, artifact.StorageRoot, artifact.ID, artifact.SHA256)
	} else {
		artifact.ObjectKey, err = domain.SourceArtifactObjectKeyForRoot(artifact.TenantID, artifact.StorageRoot, artifact.SHA256)
	}
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func performSourceMembershipConcurrentOperation(ctx context.Context, repo *GormRepository, operation string, artifact domain.SourceArtifact, requestIndex int, limits SourceArtifactLimits) (*domain.SourceArtifact, error) {
	switch operation {
	case "request":
		return repo.CreateSourceArtifactForRequestWithLimits(ctx, &artifact, fmt.Sprintf("source-request-%024x", requestIndex), limits)
	case "reopen":
		return repo.ReopenSourceArtifactUploadWithLimits(ctx, artifact.TenantID, artifact.UserID, artifact.ID, time.Now().UTC().Add(time.Hour), limits)
	default:
		return repo.CreateOrReuseSourceArtifactWithLimits(ctx, &artifact, limits)
	}
}

func waitSourceMembershipPostgresLock(t *testing.T, ctx context.Context, observer *gorm.DB, pid int, table string, results <-chan sourceMembershipConcurrentResult) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := observer.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = ? AND wait_event_type = 'Lock' AND query LIKE ?)", pid, "%\""+table+"\"%").Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case result := <-results:
			t.Fatalf("upload returned before expected %s lock wait: artifact=%v err=%v", table, result.artifact, result.err)
		case <-ctx.Done():
			t.Fatalf("upload never waited for %s row lock: %v", table, ctx.Err())
		case <-ticker.C:
		}
	}
}
