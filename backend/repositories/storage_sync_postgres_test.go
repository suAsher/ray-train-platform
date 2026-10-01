package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	databasepkg "ray-train-platform-backend/db"
	ss "ray-train-platform-backend/storagesync"
)

func storageSyncPostgresStores(t *testing.T, upgrade bool) (*StorageSyncRepository, *StorageSyncRepository) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("storage_sync_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	a, b := openArtifactPostgresConnection(t, dsn), openArtifactPostgresConnection(t, dsn)
	for _, database := range []*gorm.DB{a, b} {
		if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	if upgrade {
		if err := a.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").Error; err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir("../db/migrations")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".up.sql") {
				continue
			}
			version, err := strconv.Atoi(file.Name()[:4])
			if err != nil {
				t.Fatal(err)
			}
			if version > 57 {
				continue
			}
			raw, err := os.ReadFile(filepath.Join("../db/migrations", file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec(string(raw)).Error; err != nil {
					return err
				}
				return tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", version).Error
			}); err != nil {
				t.Fatalf("historical migration %d: %v", version, err)
			}
		}
		if err := a.Exec("INSERT INTO model_catalog(id, name, owner_id, tenant_id) VALUES ('storage-sync-kept-model', 'unchanged', 'stable-owner', 'existing-team')").Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := databasepkg.ApplyMigrations(a); err != nil {
			t.Fatal(err)
		}
	}
	if upgrade {
		var value string
		if err := a.Raw("SELECT name FROM model_catalog WHERE id = 'storage-sync-kept-model' AND owner_id = 'stable-owner' AND tenant_id = 'existing-team'").Scan(&value).Error; err != nil || value != "unchanged" {
			t.Fatalf("upgrade changed existing data: %q %v", value, err)
		}
	}
	return NewStorageSyncRepository(a), NewStorageSyncRepository(b)
}

func storageSyncPlan(id string) ss.Plan {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return ss.Plan{ID: id, Revision: 1, Name: id, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
}

func storageSyncRun(id, planID string) ss.Run {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return ss.Run{ID: id, PlanID: planID, ConfigRevision: 1, RequestedBy: "admin", IdempotencyKey: "request-" + id, State: "QUEUED", Trigger: "MANUAL", Attempt: 1, Generation: 1, Sequence: 0, Resolved: []ss.ResolvedMapping{{Source: ss.ResolvedLocation{Kind: "TOS", StorageID: "source", Bucket: "source-bucket", Prefix: "root/source"}}}, ResolutionDigest: "resolution", SourceFingerprint: "source", TargetFingerprint: "target", CreatedAt: now, UpdatedAt: now}
}

func storageSyncSeed(t *testing.T, store *StorageSyncRepository, id string) ss.Run {
	t.Helper()
	run := storageSyncRun("run-"+id, id)
	if err := store.Transact(context.Background(), func(tx ss.Tx) error {
		if err := tx.PutPlan(storageSyncPlan(id)); err != nil {
			return err
		}
		return tx.PutRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStorageSyncPostgresUpgradeSnapshotsAndRollback(t *testing.T) {
	store, reader := storageSyncPostgresStores(t, true)
	ctx := context.Background()
	run := storageSyncSeed(t, store, "roundtrip")
	preview := ss.Preview{ID: "preview", PlanID: run.PlanID, Actor: "admin", ConfigRevision: 1, State: "SUCCEEDED", Resolved: run.Resolved, ResolutionDigest: "resolution", SourceFingerprint: "source", TargetFingerprint: "target", Attempt: 1, Generation: 2, Sequence: 3, JobUID: "preview-job", ReceiptState: "SUCCEEDED", RequestsDrained: true, Cursor: "opaque-cursor", Files: ss.FileReference{Path: "previews/preview/files.json", Digest: "file-digest", Count: 1}, CreatedAt: run.CreatedAt, ExpiresAt: run.CreatedAt.Add(time.Hour)}
	run.JobUID, run.ReceiptState = "run-job", "SUCCEEDED"
	run.WorkerID, run.BaselineRef, run.LastReportDigest, run.AuthorizedBy = "worker", "baseline", "receipt-digest", "taking-admin"
	preview.WorkerID, preview.BaselineRef, preview.LastReportDigest = "preview-worker", "preview-baseline", "preview-receipt"
	run.RequestsDrained, run.StopVerified = true, true
	run.Files = ss.FileReference{Path: "runs/roundtrip/files.json", Digest: "run-file-digest", Count: 1}
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		if err := tx.PutPreview(preview); err != nil {
			return err
		}
		return tx.PutRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	gotPreview, err := reader.GetPreview(ctx, preview.ID)
	if err != nil || len(gotPreview.Resolved) != 1 || !gotPreview.ExpiresAt.Equal(preview.ExpiresAt) {
		t.Fatalf("preview snapshot lost server resolution: %+v %v", gotPreview, err)
	}
	if gotPreview.JobUID != preview.JobUID || gotPreview.Generation != 2 || gotPreview.Sequence != 3 || gotPreview.Attempt != 1 || !gotPreview.RequestsDrained || gotPreview.Cursor != preview.Cursor || gotPreview.Files.Path != preview.Files.Path || gotPreview.ReceiptState != preview.ReceiptState || gotPreview.ResolutionDigest != "resolution" || gotPreview.SourceFingerprint != "source" || gotPreview.TargetFingerprint != "target" {
		t.Fatalf("preview internal evidence lost: %+v", gotPreview)
	}
	if gotPreview.WorkerID != preview.WorkerID || gotPreview.BaselineRef != preview.BaselineRef || gotPreview.LastReportDigest != preview.LastReportDigest {
		t.Fatalf("preview claim/replay identity lost: %+v", gotPreview)
	}
	gotRun, err := reader.GetRun(ctx, run.ID)
	if err != nil || len(gotRun.Resolved) != 1 {
		t.Fatalf("run snapshot lost server resolution: %+v %v", gotRun, err)
	}
	if gotRun.JobUID != run.JobUID || !gotRun.RequestsDrained || !gotRun.StopVerified || gotRun.Generation != run.Generation || gotRun.IdempotencyKey != run.IdempotencyKey || gotRun.Files.Path != run.Files.Path || gotRun.ReceiptState != run.ReceiptState || gotRun.ResolutionDigest != "resolution" || gotRun.SourceFingerprint != "source" || gotRun.TargetFingerprint != "target" || gotRun.Resolved[0].Source.Bucket != "source-bucket" {
		t.Fatalf("run internal evidence lost: %+v", gotRun)
	}
	if gotRun.WorkerID != run.WorkerID || gotRun.BaselineRef != run.BaselineRef || gotRun.LastReportDigest != run.LastReportDigest || gotRun.AuthorizedBy != run.AuthorizedBy {
		t.Fatalf("run claim/replay identity lost: %+v", gotRun)
	}
	if err := store.db.Exec(`UPDATE storage_sync_runs SET snapshot_json = jsonb_set(snapshot_json, '{private,workerID}', '"other-worker"') WHERE id = ?`, run.ID).Error; err == nil {
		t.Fatal("database accepted a second worker claim for the same run attempt")
	}
	if err := store.db.Exec(`UPDATE storage_sync_previews SET snapshot_json = jsonb_set(snapshot_json, '{private,workerID}', '"other-worker"') WHERE id = ?`, preview.ID).Error; err == nil {
		t.Fatal("database accepted a second worker claim for the same preview attempt")
	}
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		if err := tx.PutPlan(storageSyncPlan("rollback")); err != nil {
			return err
		}
		return ss.ErrConflict
	}); !errors.Is(err, ss.ErrConflict) {
		t.Fatalf("transaction returned %v", err)
	}
	if _, err := reader.GetPlan(ctx, "rollback"); !errors.Is(err, ss.ErrNotFound) {
		t.Fatalf("rolled-back plan persisted: %v", err)
	}
	plan, err := reader.GetPlan(ctx, run.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan.Revision++
	plan.Name = "revision two"
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutPlan(plan) }); err != nil {
		t.Fatal(err)
	}
	var revisions int64
	if err := store.db.Table("storage_sync_plan_revisions").Where("plan_id = ?", plan.ID).Count(&revisions).Error; err != nil || revisions != 2 {
		t.Fatalf("immutable revisions count=%d err=%v", revisions, err)
	}
	if err := store.db.Exec("UPDATE storage_sync_plan_revisions SET snapshot_json = '{}' WHERE plan_id = ?", plan.ID).Error; err == nil {
		t.Fatal("historical plan revision can be rewritten")
	}
}

func TestStorageSyncPostgresPlanLockSerializesMutations(t *testing.T) {
	first, second := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	run := storageSyncSeed(t, first, "serialize")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- first.Transact(ctx, func(tx ss.Tx) error {
			plan, err := tx.GetPlan(run.PlanID)
			if err != nil {
				return err
			}
			close(entered)
			<-release
			plan.Revision++
			return tx.PutPlan(plan)
		})
	}()
	<-entered
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.Transact(ctx, func(tx ss.Tx) error {
			plan, err := tx.GetPlan(run.PlanID)
			if err != nil {
				return err
			}
			if plan.Revision != 2 {
				return fmt.Errorf("revision after lock = %d", plan.Revision)
			}
			return nil
		})
	}()
	select {
	case err := <-secondDone:
		close(release)
		<-done
		t.Fatalf("second transaction completed before first committed: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestStorageSyncPostgresRunUniquenessAndCallbackFence(t *testing.T) {
	store, reader := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	run := storageSyncSeed(t, store, "unique")
	duplicate := storageSyncRun("duplicate", run.PlanID)
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(duplicate) }); !errors.Is(err, ss.ErrConflict) {
		t.Fatalf("parallel run for same plan accepted: %v", err)
	}
	run.State, run.Sequence = "RUNNING", 1
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(run) }); err != nil {
		t.Fatal(err)
	}
	stale := run
	run.Attempt, run.Generation, run.Sequence = 2, 2, 0
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(run) }); err != nil {
		t.Fatal(err)
	}
	stale.Sequence = 50
	stale.State = "SUCCEEDED"
	if err := reader.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(stale) }); !errors.Is(err, ss.ErrConflict) {
		t.Fatalf("old-attempt callback accepted: %v", err)
	}
	run.Sequence = 2
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(run) }); err != nil {
		t.Fatal(err)
	}
	stale = run
	stale.Sequence = 1
	if err := reader.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(stale) }); !errors.Is(err, ss.ErrConflict) {
		t.Fatalf("out-of-order callback accepted: %v", err)
	}
	finished := time.Now().UTC()
	run.State, run.FinishedAt = "SUCCEEDED", &finished
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(run) }); err != nil {
		t.Fatal(err)
	}
	duplicate.IdempotencyKey = run.IdempotencyKey
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(duplicate) }); !errors.Is(err, ss.ErrConflict) {
		t.Fatalf("duplicate request persisted: %v", err)
	}
	var attempts int64
	if err := store.db.Table("storage_sync_attempts").Where("run_id = ?", run.ID).Count(&attempts).Error; err != nil || attempts != 2 {
		t.Fatalf("attempt history count=%d err=%v", attempts, err)
	}
}

func TestStorageSyncPostgresHierarchicalReadWriteLocks(t *testing.T) {
	store, other := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	left := storageSyncSeed(t, store, "left")
	right := storageSyncSeed(t, store, "right")
	lock := func(prefix, mode string) ss.PathLock {
		return ss.PathLock{StorageID: "tos-root", Region: "region", Bucket: "bucket", Prefix: prefix, Mode: mode}
	}
	acquire := func(repo *StorageSyncRepository, run ss.Run, locks ...ss.PathLock) error {
		return repo.Transact(ctx, func(tx ss.Tx) error { return tx.AcquireLocks(run.ID, run.Attempt, locks) })
	}
	if err := acquire(store, left, lock("a/", "READ")); err != nil {
		t.Fatal(err)
	}
	if err := acquire(other, right, lock("a/b/", "READ")); err != nil {
		t.Fatalf("shared read locks rejected: %v", err)
	}
	if err := other.Transact(ctx, func(tx ss.Tx) error { return tx.ReleaseLocks(right.ID) }); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "a", "a/b", "a/"} {
		if err := acquire(other, right, lock(prefix, "WRITE")); !errors.Is(err, ss.ErrLocked) {
			t.Fatalf("overlap %q did not conflict: %v", prefix, err)
		}
	}
	if err := acquire(other, right, lock("ab/", "WRITE")); err != nil {
		t.Fatalf("sibling prefix unexpectedly overlaps: %v", err)
	}
	if err := store.Transact(ctx, func(tx ss.Tx) error { return tx.ReleaseLocks(left.ID) }); err != nil {
		t.Fatal(err)
	}
	if err := other.Transact(ctx, func(tx ss.Tx) error { return tx.ReleaseLocks(right.ID) }); err != nil {
		t.Fatal(err)
	}
	if err := acquire(other, right, lock("a/", "WRITE")); err != nil {
		t.Fatal(err)
	}
	if err := acquire(store, left, lock("a/b/", "READ")); !errors.Is(err, ss.ErrLocked) {
		t.Fatalf("read source overlapping active target accepted: %v", err)
	}
	if err := acquire(store, left, lock("x/", "WRITE"), lock("a/", "WRITE")); !errors.Is(err, ss.ErrLocked) {
		t.Fatalf("atomic multi-lock acquisition returned %v", err)
	}
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		if err := tx.AcquireLocks(left.ID, left.Attempt, []ss.PathLock{lock("x/", "WRITE"), lock("a/", "WRITE")}); !errors.Is(err, ss.ErrLocked) {
			return fmt.Errorf("handled lock conflict returned %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var partial int64
	if err := store.db.Table("storage_sync_path_locks").Where("run_id = ?", left.ID).Count(&partial).Error; err != nil || partial != 0 {
		t.Fatalf("failed acquisition retained %d locks: %v", partial, err)
	}
}

func TestStorageSyncPostgresConcurrentIdempotentTrigger(t *testing.T) {
	first, second := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	plan := storageSyncPlan("idempotent")
	if err := first.Transact(ctx, func(tx ss.Tx) error { return tx.PutPlan(plan) }); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  string
		err error
	}
	start, results := make(chan struct{}), make(chan result, 2)
	for i, store := range []*StorageSyncRepository{first, second} {
		go func(i int, store *StorageSyncRepository) {
			<-start
			var id string
			err := store.Transact(ctx, func(tx ss.Tx) error {
				if _, err := tx.GetPlan(plan.ID); err != nil {
					return err
				}
				runs, err := tx.ListRuns(plan.ID)
				if err != nil {
					return err
				}
				for _, run := range runs {
					if run.IdempotencyKey == "same-request" {
						id = run.ID
						return nil
					}
				}
				run := storageSyncRun(fmt.Sprintf("candidate-%d", i), plan.ID)
				run.IdempotencyKey = "same-request"
				id = run.ID
				return tx.PutRun(run)
			})
			results <- result{id, err}
		}(i, store)
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.id == "" || a.id != b.id {
		t.Fatalf("idempotent race: %+v %+v", a, b)
	}
	runs, err := first.ListRuns(ctx, plan.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("trigger persisted %d runs: %v", len(runs), err)
	}
}

func TestStorageSyncPostgresFileResultsArePagedAndFenced(t *testing.T) {
	store, reader := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	run := storageSyncSeed(t, store, "files")
	files := []ss.FileResult{
		{MappingIndex: 1, RelativePath: "a.bin", State: "VERIFIED", SizeBytes: 8},
		{MappingIndex: 0, RelativePath: "b.bin", State: "REUSED", SizeBytes: 4},
		{MappingIndex: 0, RelativePath: "a.bin", State: "FAILED", ErrorCode: "SOURCE_CHANGED"},
	}
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		return tx.PutFileResults(run.ID, run.Attempt, run.Generation, files)
	}); err != nil {
		t.Fatal(err)
	}
	page, err := reader.ListRunFiles(ctx, run.ID, "", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].MappingIndex != 0 || page.Items[1].MappingIndex != 0 || page.Items[0].RelativePath == page.Items[1].RelativePath || page.NextCursor == "" {
		t.Fatalf("first file page %+v %v", page, err)
	}
	last, err := reader.ListRunFiles(ctx, run.ID, page.NextCursor, 2)
	if err != nil || len(last.Items) != 1 || last.Items[0].MappingIndex != 1 || last.NextCursor != "" {
		t.Fatalf("second file page %+v %v", last, err)
	}
	if err := reader.Transact(ctx, func(tx ss.Tx) error {
		return tx.PutFileResults(run.ID, run.Attempt, run.Generation+1, files)
	}); !errors.Is(err, ss.ErrConflict) {
		t.Fatalf("wrong generation indexed files: %v", err)
	}
	if _, err := reader.ListRunFiles(ctx, run.ID, "invalid", 2); !errors.Is(err, ss.ErrInvalid) {
		t.Fatalf("invalid file cursor accepted: %v", err)
	}
	longPath := strings.Repeat("long-directory/", 200) + "file.bin"
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		return tx.PutFileResults(run.ID, run.Attempt, run.Generation, []ss.FileResult{{MappingIndex: 2, RelativePath: longPath, State: "VERIFIED"}})
	}); err != nil {
		t.Fatalf("long relative path exceeded database index limits: %v", err)
	}
	all, err := reader.ListRunFiles(ctx, run.ID, "", 100)
	if err != nil || len(all.Items) != 4 || all.Items[3].RelativePath != longPath {
		t.Fatalf("long path roundtrip lost: %+v %v", all, err)
	}
	if err := reader.Transact(ctx, func(tx ss.Tx) error {
		return tx.PutFileResults(run.ID, run.Attempt, run.Generation, []ss.FileResult{{RelativePath: "../escape", State: "VERIFIED"}})
	}); !errors.Is(err, ss.ErrInvalid) {
		t.Fatalf("invalid file result accepted: %v", err)
	}
}

func TestStorageSyncPostgresStagedLocksAndVerifiedRetry(t *testing.T) {
	store, reader := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	plan := storageSyncPlan("reserve-first")
	run := storageSyncRun("reserve-run", plan.ID)
	locks := []ss.PathLock{{StorageID: "root", Region: "region", Bucket: "bucket", Prefix: "a", Mode: "WRITE"}}
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		if err := tx.PutPlan(plan); err != nil {
			return err
		}
		if err := tx.AcquireLocks(run.ID, run.Attempt, locks); err != nil {
			return err
		}
		return tx.PutRun(run)
	}); err != nil {
		t.Fatalf("lock-before-run reservation failed: %v", err)
	}
	finished := time.Now().UTC()
	run.State, run.StopVerified, run.FinishedAt = "FAILED", true, &finished
	if err := store.Transact(ctx, func(tx ss.Tx) error {
		if err := tx.ReleaseLocks(run.ID); err != nil {
			return err
		}
		return tx.PutRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	run.Attempt, run.Generation = 2, 2
	run.State, run.StopVerified, run.FinishedAt = "QUEUED", false, nil
	if err := reader.Transact(ctx, func(tx ss.Tx) error {
		if err := tx.AcquireLocks(run.ID, run.Attempt, locks); err != nil {
			return err
		}
		return tx.PutRun(run)
	}); err != nil {
		t.Fatalf("verified failed run could not retry: %v", err)
	}
	var attempt int
	if err := store.db.Table("storage_sync_path_locks").Select("attempt").Where("run_id = ?", run.ID).Scan(&attempt).Error; err != nil || attempt != 2 {
		t.Fatalf("lock attempt %d %v", attempt, err)
	}
}
