package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
)

const validMLflowDashboardTokenHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func mlflowDashboardTestRepositories(t *testing.T) (*GormRepository, *GormRepository) {
	t.Helper()

	databasePath := filepath.Join(t.TempDir(), "mlflow-dashboard.db")
	dsn := "file:" + databasePath + "?_busy_timeout=5000&_journal_mode=WAL"
	open := func() *gorm.DB {
		database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatalf("open MLflow dashboard database: %v", err)
		}
		sqlDatabase, err := database.DB()
		if err != nil {
			t.Fatalf("get MLflow dashboard SQL database: %v", err)
		}
		sqlDatabase.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if err := sqlDatabase.Close(); err != nil {
				t.Errorf("close MLflow dashboard database: %v", err)
			}
		})
		return database
	}

	firstDatabase := open()
	if err := firstDatabase.AutoMigrate(&MLflowDashboardTicketRecord{}, &AuditLogRecord{}); err != nil {
		t.Fatalf("migrate MLflow dashboard database: %v", err)
	}
	return NewGormRepository(firstDatabase), NewGormRepository(open())
}

func validMLflowDashboardTicket(now time.Time) MLflowDashboardTicketRecord {
	return MLflowDashboardTicketRecord{
		TokenHash: validMLflowDashboardTokenHash,
		TenantID:  "tenant-a",
		UserID:    "oidc-subject-a",
		ExpiresAt: now.Add(time.Minute),
		CreatedAt: now,
	}
}

func TestMLflowDashboardTicketCanOnlyBeConsumedOnce(t *testing.T) {
	repository, _ := mlflowDashboardTestRepositories(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ticket := validMLflowDashboardTicket(now)

	if err := repository.CreateMLflowDashboardTicket(ctx, ticket); err != nil {
		t.Fatalf("create MLflow dashboard ticket: %v", err)
	}
	consumed, err := repository.ConsumeMLflowDashboardTicket(ctx, ticket.TokenHash, now)
	if err != nil {
		t.Fatalf("consume MLflow dashboard ticket: %v", err)
	}
	if consumed.TokenHash != ticket.TokenHash || consumed.TenantID != ticket.TenantID || consumed.UserID != ticket.UserID {
		t.Fatalf("unexpected consumed ticket: %+v", consumed)
	}
	if consumed.ConsumedAt == nil || !consumed.ConsumedAt.Equal(now) {
		t.Fatalf("consumed_at = %v, want %v", consumed.ConsumedAt, now)
	}

	if _, err := repository.ConsumeMLflowDashboardTicket(ctx, ticket.TokenHash, now); !errors.Is(err, ErrMLflowDashboardTicketInvalid) {
		t.Fatalf("second consume error = %v, want ErrMLflowDashboardTicketInvalid", err)
	}
}

func TestMLflowDashboardTicketValidation(t *testing.T) {
	repository, _ := mlflowDashboardTestRepositories(t)
	ctx := context.Background()
	now := time.Now().UTC()

	tests := []struct {
		name   string
		mutate func(*MLflowDashboardTicketRecord)
	}{
		{name: "short token hash", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.TokenHash = strings.Repeat("a", 63) }},
		{name: "token hash with whitespace", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.TokenHash = " " + ticket.TokenHash }},
		{name: "upper-case token hash", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.TokenHash = strings.ToUpper(ticket.TokenHash) }},
		{name: "non-hex token hash", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.TokenHash = strings.Repeat("z", 64) }},
		{name: "blank tenant", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.TenantID = " \t" }},
		{name: "blank user", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.UserID = "\n" }},
		{name: "expiry equals creation", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.ExpiresAt = ticket.CreatedAt }},
		{name: "expiry before creation", mutate: func(ticket *MLflowDashboardTicketRecord) { ticket.ExpiresAt = ticket.CreatedAt.Add(-time.Second) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := validMLflowDashboardTicket(now)
			test.mutate(&ticket)
			if err := repository.CreateMLflowDashboardTicket(ctx, ticket); !errors.Is(err, ErrMLflowDashboardTicketInvalid) {
				t.Fatalf("create error = %v, want ErrMLflowDashboardTicketInvalid", err)
			}
		})
	}

	var count int64
	if err := repository.db.Model(&MLflowDashboardTicketRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("count invalid tickets: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid ticket rows = %d, want 0", count)
	}
}

func TestMLflowDashboardTicketConsumeRejectsInvalidTickets(t *testing.T) {
	repository, _ := mlflowDashboardTestRepositories(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	expired := validMLflowDashboardTicket(now.Add(-2 * time.Minute))
	if err := repository.CreateMLflowDashboardTicket(ctx, expired); err != nil {
		t.Fatalf("create expired-at-consume ticket: %v", err)
	}
	valid := validMLflowDashboardTicket(now)
	valid.TokenHash = strings.Repeat("b", 64)
	if err := repository.CreateMLflowDashboardTicket(ctx, valid); err != nil {
		t.Fatalf("create valid ticket for malformed consume: %v", err)
	}

	for _, tokenHash := range []string{
		expired.TokenHash,
		strings.Repeat("c", 64),
		"raw-ticket-secret",
		strings.ToUpper(validMLflowDashboardTokenHash),
		" " + valid.TokenHash,
	} {
		if _, err := repository.ConsumeMLflowDashboardTicket(ctx, tokenHash, now); !errors.Is(err, ErrMLflowDashboardTicketInvalid) {
			t.Errorf("consume %q error = %v, want ErrMLflowDashboardTicketInvalid", tokenHash, err)
		}
	}
}

func TestMLflowDashboardTicketConsumeRejectsExactExpiryBoundary(t *testing.T) {
	repository, _ := mlflowDashboardTestRepositories(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ticket := validMLflowDashboardTicket(now.Add(-time.Minute))
	if !ticket.ExpiresAt.Equal(now) {
		t.Fatalf("test ticket expiry = %v, want exact boundary %v", ticket.ExpiresAt, now)
	}
	if err := repository.CreateMLflowDashboardTicket(ctx, ticket); err != nil {
		t.Fatalf("create exact-boundary MLflow dashboard ticket: %v", err)
	}

	if _, err := repository.ConsumeMLflowDashboardTicket(ctx, ticket.TokenHash, now); !errors.Is(err, ErrMLflowDashboardTicketInvalid) {
		t.Fatalf("consume at exact expiry error = %v, want ErrMLflowDashboardTicketInvalid", err)
	}
	var stored MLflowDashboardTicketRecord
	if err := repository.db.First(&stored, "token_hash = ?", ticket.TokenHash).Error; err != nil {
		t.Fatalf("load exact-boundary MLflow dashboard ticket: %v", err)
	}
	if stored.ConsumedAt != nil {
		t.Fatalf("exact-boundary ticket transitioned consumed_at to %v", stored.ConsumedAt)
	}
}

func TestMLflowDashboardTicketConcurrentConsumeHasExactlyOneWinner(t *testing.T) {
	first, second := mlflowDashboardTestRepositories(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ticket := validMLflowDashboardTicket(now)
	if err := first.CreateMLflowDashboardTicket(ctx, ticket); err != nil {
		t.Fatalf("create concurrent MLflow dashboard ticket: %v", err)
	}

	start := make(chan struct{})
	errorsByConsumer := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, repository := range []*GormRepository{first, second} {
		go func(repository *GormRepository) {
			ready.Done()
			<-start
			_, err := repository.ConsumeMLflowDashboardTicket(ctx, ticket.TokenHash, now)
			errorsByConsumer <- err
		}(repository)
	}
	ready.Wait()
	close(start)

	successes := 0
	invalid := 0
	for range 2 {
		err := <-errorsByConsumer
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrMLflowDashboardTicketInvalid):
			invalid++
		default:
			t.Fatalf("unexpected concurrent consume error: %v", err)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent consumes: successes=%d invalid=%d, want 1 and 1", successes, invalid)
	}
}

func TestMLflowDashboardAuditStoresOnlyAllowlistedNormalizedMetadata(t *testing.T) {
	repository, _ := mlflowDashboardTestRepositories(t)
	sensitiveQueryValue := "must-not-be-audited"
	event := MLflowAuditEvent{
		Principal: auth.Principal{
			Subject: "oidc-subject-a", Username: "alice", TenantID: "tenant-a", AuthType: auth.AuthTypeOIDC,
		},
		Method:    " post ",
		Path:      "ajax-api/2.0/mlflow/runs/search?q=" + sensitiveQueryValue + "#fragment",
		Status:    502,
		Duration:  1500*time.Millisecond + 999*time.Microsecond,
		RequestID: "request-123",
	}
	if err := repository.CreateMLflowAuditLog(context.Background(), event); err != nil {
		t.Fatalf("create MLflow audit log: %v", err)
	}

	var record AuditLogRecord
	if err := repository.db.First(&record).Error; err != nil {
		t.Fatalf("load MLflow audit log: %v", err)
	}
	if record.TenantID != "tenant-a" || record.UserID != "oidc-subject-a" || record.Action != "mlflow.dashboard.proxy" || record.ResourceType != "mlflow" {
		t.Fatalf("unexpected MLflow audit identity: %+v", record)
	}
	if record.ResourceID != "/ajax-api/2.0/mlflow/runs/search" || record.RequestID != "request-123" {
		t.Fatalf("unexpected MLflow audit resource: resource_id=%q request_id=%q", record.ResourceID, record.RequestID)
	}
	if strings.Contains(record.PayloadJSON, sensitiveQueryValue) || strings.Contains(record.PayloadJSON, "q=") || strings.Contains(record.PayloadJSON, "fragment") {
		t.Fatalf("secret query data leaked into audit payload: %s", record.PayloadJSON)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(record.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode MLflow audit payload: %v", err)
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{"actor_username", "auth_type", "duration_ms", "method", "outcome", "path", "status"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("audit payload keys = %v, want %v", keys, wantKeys)
	}
	if payload["actor_username"] != "alice" || payload["auth_type"] != "oidc" || payload["outcome"] != "failure" || payload["method"] != "POST" || payload["path"] != record.ResourceID {
		t.Fatalf("unexpected MLflow audit payload: %#v", payload)
	}
	if payload["status"] != float64(502) || payload["duration_ms"] != float64(1500) {
		t.Fatalf("unexpected numeric MLflow audit payload: %#v", payload)
	}
}

func TestMLflowDashboardAuditCapsUntrustedText(t *testing.T) {
	repository, _ := mlflowDashboardTestRepositories(t)
	event := MLflowAuditEvent{
		Principal: auth.Principal{
			Subject: strings.Repeat("s", 1024), Username: strings.Repeat("u", 1024), TenantID: strings.Repeat("t", 1024), AuthType: auth.AuthenticationType(strings.Repeat("a", 1024)),
		},
		Method:    strings.Repeat("p", 1024),
		Path:      strings.Repeat("segment/", 1024),
		Status:    200,
		RequestID: strings.Repeat("r", 1024),
	}
	if err := repository.CreateMLflowAuditLog(context.Background(), event); err != nil {
		t.Fatalf("create capped MLflow audit log: %v", err)
	}

	var record AuditLogRecord
	if err := repository.db.First(&record).Error; err != nil {
		t.Fatalf("load capped MLflow audit log: %v", err)
	}
	if len(record.TenantID) > 128 || len(record.UserID) > 128 || len(record.RequestID) > 128 || len(record.ResourceID) > 2048 {
		t.Fatalf("uncapped audit record lengths: tenant=%d user=%d request=%d resource=%d", len(record.TenantID), len(record.UserID), len(record.RequestID), len(record.ResourceID))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(record.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode capped MLflow audit payload: %v", err)
	}
	if len(payload["actor_username"].(string)) > 128 || len(payload["auth_type"].(string)) > 32 || len(payload["method"].(string)) > 16 || len(payload["path"].(string)) > 2048 {
		t.Fatalf("uncapped audit payload: %#v", payload)
	}
}

func TestMLflowDashboardTicketMigrationHasTableAndExpiryIndexWithoutForeignKeys(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "db", "migrations", "0019_mlflow_dashboard_tickets.up.sql"))
	if err != nil {
		t.Fatalf("read MLflow dashboard ticket migration: %v", err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"create table if not exists mlflow_dashboard_tickets",
		"token_hash text primary key",
		"tenant_id text not null",
		"user_id text not null",
		"expires_at timestamptz not null",
		"consumed_at timestamptz",
		"created_at timestamptz not null default now()",
		"create index if not exists idx_mlflow_dashboard_tickets_expiry on mlflow_dashboard_tickets(expires_at)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	if strings.Contains(sql, "references ") || strings.Contains(sql, "foreign key") {
		t.Fatal("MLflow dashboard tickets must not reference local identity tables")
	}
}
