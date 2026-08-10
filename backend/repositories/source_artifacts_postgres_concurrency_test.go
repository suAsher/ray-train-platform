package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
)

func TestSourceArtifactRepositoryPostgresReopenAndPendingQuotaAreAtomic(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("artifact_quota_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if err := admin.Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	firstDB := openArtifactPostgresConnection(t, dsn)
	secondDB := openArtifactPostgresConnection(t, dsn)
	for index, database := range []*gorm.DB{firstDB, secondDB} {
		if err := database.Exec("SET search_path TO " + quotedSchema).Error; err != nil {
			t.Fatalf("set search path on connection %d: %v", index+1, err)
		}
	}
	if err := databasepkg.ApplyMigrations(firstDB); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	first, second := NewGormRepository(firstDB), NewGormRepository(secondDB)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)

	reopenPrincipal := auth.Principal{Subject: "reopen-user", TenantID: "atomic-tenant", Username: "reopen-user", Roles: []string{"Engineer"}}
	if err := first.EnsureIdentity(ctx, reopenPrincipal); err != nil {
		t.Fatalf("ensure reopen identity: %v", err)
	}
	reopenFixture := mustPostgresArtifact(t, "reopen-fixture", reopenPrincipal, strings.Repeat("a", 64), now.Add(15*time.Minute), now)
	created, err := first.CreateOrReuseSourceArtifact(ctx, &reopenFixture)
	if err != nil {
		t.Fatalf("create reopen fixture: %v", err)
	}
	if _, err := first.MarkSourceArtifactReady(ctx, reopenPrincipal.TenantID, reopenPrincipal.Subject, created.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("mark reopen fixture ready: %v", err)
	}

	type artifactResult struct {
		artifact *domain.SourceArtifact
		err      error
	}
	reopenStart := make(chan struct{})
	reopenResults := make(chan artifactResult, 2)
	for index, repo := range []*GormRepository{first, second} {
		expires := now.Add(time.Duration(20+index) * time.Minute)
		go func(repository *GormRepository, expiry time.Time) {
			<-reopenStart
			artifact, reopenErr := repository.ReopenSourceArtifactUpload(ctx, reopenPrincipal.TenantID, reopenPrincipal.Subject, created.ID, expiry)
			reopenResults <- artifactResult{artifact: artifact, err: reopenErr}
		}(repo, expires)
	}
	close(reopenStart)
	for index := 0; index < 2; index++ {
		result := <-reopenResults
		if result.err != nil || result.artifact == nil || result.artifact.ID != created.ID || result.artifact.State != domain.SourceArtifactPending || result.artifact.CompletedAt != nil {
			t.Fatalf("concurrent reopen result %d: id=%q state=%q completed=%t err=%v", index+1, artifactID(result.artifact), artifactState(result.artifact), artifactCompleted(result.artifact), result.err)
		}
	}

	quotaPrincipal := auth.Principal{Subject: "quota-user", TenantID: "atomic-tenant", Username: "quota-user", Roles: []string{"Engineer"}}
	if err := first.EnsureIdentity(ctx, quotaPrincipal); err != nil {
		t.Fatalf("ensure quota identity: %v", err)
	}
	left := mustPostgresArtifact(t, "quota-left", quotaPrincipal, strings.Repeat("b", 64), now.Add(15*time.Minute), now)
	right := mustPostgresArtifact(t, "quota-right", quotaPrincipal, strings.Repeat("c", 64), now.Add(15*time.Minute), now)
	limits := SourceArtifactLimits{MaxPending: 1, QuotaBytes: DefaultSourceArtifactQuotaBytes}
	quotaStart := make(chan struct{})
	quotaResults := make(chan artifactResult, 2)
	for index, input := range []*domain.SourceArtifact{&left, &right} {
		repository := first
		if index == 1 {
			repository = second
		}
		go func(repo *GormRepository, artifact *domain.SourceArtifact) {
			<-quotaStart
			stored, createErr := repo.CreateOrReuseSourceArtifactWithLimits(ctx, artifact, limits)
			quotaResults <- artifactResult{artifact: stored, err: createErr}
		}(repository, input)
	}
	close(quotaStart)
	successes, rejected := 0, 0
	for index := 0; index < 2; index++ {
		result := <-quotaResults
		switch {
		case result.err == nil:
			successes++
		case errors.Is(result.err, ErrSourceArtifactQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected quota race error: %v", result.err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("quota race successes=%d rejected=%d, want 1/1", successes, rejected)
	}
	var pending int64
	if err := firstDB.Model(&SourceArtifactRecord{}).Where("tenant_id = ? AND user_id = ? AND state = ?", quotaPrincipal.TenantID, quotaPrincipal.Subject, string(domain.SourceArtifactPending)).Count(&pending).Error; err != nil {
		t.Fatalf("count quota artifacts: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending artifacts=%d, want 1", pending)
	}
}

func artifactID(artifact *domain.SourceArtifact) string {
	if artifact == nil {
		return ""
	}
	return artifact.ID
}

func artifactCompleted(artifact *domain.SourceArtifact) bool {
	return artifact != nil && artifact.CompletedAt != nil
}

func TestSourceArtifactRepositoryPostgresConcurrentCreateAndReadyRefresh(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("artifact_concurrency_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if err := admin.Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	firstDB := openArtifactPostgresConnection(t, dsn)
	secondDB := openArtifactPostgresConnection(t, dsn)
	markReadyDB := openArtifactPostgresConnection(t, dsn)
	for index, database := range []*gorm.DB{firstDB, secondDB, markReadyDB} {
		if err := database.Exec("SET search_path TO " + quotedSchema).Error; err != nil {
			t.Fatalf("set search path on connection %d: %v", index+1, err)
		}
	}
	if err := databasepkg.ApplyMigrations(firstDB); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	first := NewGormRepository(firstDB)
	second := NewGormRepository(secondDB)
	marker := NewGormRepository(markReadyDB)
	principal := auth.Principal{Subject: "concurrent-user", TenantID: "concurrent-tenant", Username: "concurrent-user", Roles: []string{"Engineer"}}
	if err := first.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	digest := strings.Repeat("d", 64)
	firstArtifact := mustPostgresArtifact(t, "concurrent-a", principal, digest, now.Add(15*time.Minute), now)
	secondArtifact := mustPostgresArtifact(t, "concurrent-b", principal, digest, now.Add(16*time.Minute), now)
	type createResult struct {
		artifact *domain.SourceArtifact
		err      error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	go func() {
		<-start
		artifact, err := first.CreateOrReuseSourceArtifact(ctx, &firstArtifact)
		results <- createResult{artifact: artifact, err: err}
	}()
	go func() {
		<-start
		artifact, err := second.CreateOrReuseSourceArtifact(ctx, &secondArtifact)
		results <- createResult{artifact: artifact, err: err}
	}()
	close(start)
	left, right := <-results, <-results
	for _, result := range []createResult{left, right} {
		if result.err != nil {
			t.Fatalf("concurrent create/reuse: %v", result.err)
		}
	}
	if left.artifact.ID != right.artifact.ID {
		t.Fatalf("concurrent create produced different IDs: %q vs %q", left.artifact.ID, right.artifact.ID)
	}

	raceDigest := strings.Repeat("e", 64)
	pending := mustPostgresArtifact(t, "race-artifact", principal, raceDigest, now.Add(15*time.Minute), now)
	created, err := first.CreateOrReuseSourceArtifact(ctx, &pending)
	if err != nil {
		t.Fatalf("create race fixture: %v", err)
	}
	refresh := mustPostgresArtifact(t, "race-refresh", principal, raceDigest, now.Add(17*time.Minute), now)

	callbackName := "test:block_source_artifact_ready_update"
	readyUpdateEntered := make(chan struct{})
	releaseReadyUpdate := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	releaseUpdate := func() { releaseOnce.Do(func() { close(releaseReadyUpdate) }) }
	defer releaseUpdate()
	if err := markReadyDB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "source_artifacts" {
			return
		}
		enteredOnce.Do(func() { close(readyUpdateEntered) })
		select {
		case <-releaseReadyUpdate:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}); err != nil {
		t.Fatalf("register ready update callback: %v", err)
	}
	defer func() {
		if err := markReadyDB.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove ready update callback: %v", err)
		}
	}()

	readyResult := make(chan error, 1)
	go func() {
		_, err := marker.MarkSourceArtifactReady(ctx, principal.TenantID, principal.Subject, created.ID, now.Add(time.Minute))
		readyResult <- err
	}()
	select {
	case <-readyUpdateEntered:
	case <-ctx.Done():
		t.Fatalf("MarkReady did not reach blocked update after row lock: %v", ctx.Err())
	}

	refreshStarted := make(chan struct{})
	refreshResult := make(chan createResult, 1)
	go func() {
		close(refreshStarted)
		artifact, err := second.CreateOrReuseSourceArtifact(ctx, &refresh)
		refreshResult <- createResult{artifact: artifact, err: err}
	}()
	<-refreshStarted
	select {
	case result := <-refreshResult:
		t.Fatalf("refresh returned before READY transaction released its row lock: state=%v err=%v", artifactState(result.artifact), result.err)
	case <-time.After(150 * time.Millisecond):
	}

	releaseUpdate()
	select {
	case err := <-readyResult:
		if err != nil {
			t.Fatalf("mark ready race: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("MarkReady did not commit: %v", ctx.Err())
	}
	var competingRefresh createResult
	select {
	case competingRefresh = <-refreshResult:
	case <-ctx.Done():
		t.Fatalf("refresh did not resume after READY commit: %v", ctx.Err())
	}
	if competingRefresh.err != nil {
		t.Fatalf("competing refresh: %v", competingRefresh.err)
	}
	if competingRefresh.artifact.ID != created.ID || competingRefresh.artifact.State != domain.SourceArtifactReady {
		t.Fatalf("competing refresh returned stale artifact: id=%q state=%q", competingRefresh.artifact.ID, competingRefresh.artifact.State)
	}
}

func artifactState(artifact *domain.SourceArtifact) domain.SourceArtifactState {
	if artifact == nil {
		return ""
	}
	return artifact.State
}

func openArtifactPostgresConnection(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatalf("get postgres connection: %v", err)
	}
	sqlDatabase.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDatabase.Close(); err != nil {
			t.Errorf("close postgres connection: %v", err)
		}
	})
	return database
}

func mustPostgresArtifact(t *testing.T, id string, principal auth.Principal, digest string, expiresAt, now time.Time) domain.SourceArtifact {
	t.Helper()
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: id, TenantID: principal.TenantID, UserID: principal.Subject,
		SHA256: digest, SizeBytes: 100,
	}, expiresAt, now)
	if err != nil {
		t.Fatalf("new artifact: %v", err)
	}
	return artifact
}

func TestSourceArtifactRepositoryPostgresDistinctReadyReopensHonorPendingQuota(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("artifact_reopen_quota_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if err := admin.Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	firstDB := openArtifactPostgresConnection(t, dsn)
	secondDB := openArtifactPostgresConnection(t, dsn)
	for index, database := range []*gorm.DB{firstDB, secondDB} {
		if err := database.Exec("SET search_path TO " + quotedSchema).Error; err != nil {
			t.Fatalf("set search path on connection %d: %v", index+1, err)
		}
	}
	if err := databasepkg.ApplyMigrations(firstDB); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	first, second := NewGormRepository(firstDB), NewGormRepository(secondDB)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	principal := auth.Principal{Subject: "reopen-quota-user", TenantID: "reopen-quota-tenant", Username: "reopen-quota-user", Roles: []string{"Engineer"}}
	if err := first.EnsureIdentity(ctx, principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}

	ids := make([]string, 0, 2)
	for index, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		input := mustPostgresArtifact(t, fmt.Sprintf("ready-%d", index), principal, digest, now.Add(15*time.Minute), now)
		created, err := first.CreateOrReuseSourceArtifact(ctx, &input)
		if err != nil {
			t.Fatalf("create ready fixture %d: %v", index, err)
		}
		if _, err := first.MarkSourceArtifactReady(ctx, principal.TenantID, principal.Subject, created.ID, now); err != nil {
			t.Fatalf("mark ready fixture %d: %v", index, err)
		}
		ids = append(ids, created.ID)
	}

	type reopenResult struct {
		artifact *domain.SourceArtifact
		err      error
	}
	limits := SourceArtifactLimits{MaxPending: 1, QuotaBytes: DefaultSourceArtifactQuotaBytes}
	start := make(chan struct{})
	results := make(chan reopenResult, 2)
	for index, id := range ids {
		repository := first
		if index == 1 {
			repository = second
		}
		go func(repo *GormRepository, artifactID string) {
			<-start
			artifact, err := repo.ReopenSourceArtifactUploadWithLimits(ctx, principal.TenantID, principal.Subject, artifactID, now.Add(15*time.Minute), limits)
			results <- reopenResult{artifact: artifact, err: err}
		}(repository, id)
	}
	close(start)
	successes, rejected := 0, 0
	for range ids {
		result := <-results
		switch {
		case result.err == nil && result.artifact != nil && result.artifact.State == domain.SourceArtifactPending:
			successes++
		case errors.Is(result.err, ErrSourceArtifactQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected distinct reopen result: id=%q state=%q err=%v", artifactID(result.artifact), artifactState(result.artifact), result.err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("distinct reopen quota successes=%d rejected=%d, want 1/1", successes, rejected)
	}
	var pending int64
	if err := firstDB.Model(&SourceArtifactRecord{}).Where("tenant_id = ? AND user_id = ? AND state = ?", principal.TenantID, principal.Subject, string(domain.SourceArtifactPending)).Count(&pending).Error; err != nil {
		t.Fatalf("count pending reopens: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending reopens=%d, want 1", pending)
	}
}
