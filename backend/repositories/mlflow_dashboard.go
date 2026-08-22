package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm/clause"
	"ray-train-platform-backend/auth"
)

var (
	ErrMLflowDashboardTicketInvalid = errors.New("mlflow dashboard ticket is invalid")
	mlflowDashboardTokenHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	mlflowAuditIdentityMaxLength  = 128
	mlflowAuditUsernameMaxLength  = 128
	mlflowAuditAuthTypeMaxLength  = 32
	mlflowAuditMethodMaxLength    = 16
	mlflowAuditPathMaxLength      = 2048
	mlflowAuditRequestIDMaxLength = 128
)

// MLflowDashboardTicketRecord stores only the SHA-256 hash of a short-lived
// dashboard access ticket. TokenHash is intentionally the sole credential
// field so callers cannot persist the raw bearer ticket through this model.
type MLflowDashboardTicketRecord struct {
	TokenHash  string     `gorm:"column:token_hash;primaryKey"`
	TenantID   string     `gorm:"column:tenant_id"`
	UserID     string     `gorm:"column:user_id"`
	ExpiresAt  time.Time  `gorm:"column:expires_at"`
	ConsumedAt *time.Time `gorm:"column:consumed_at"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
}

func (MLflowDashboardTicketRecord) TableName() string { return "mlflow_dashboard_tickets" }

type MLflowAuditEvent struct {
	Principal auth.Principal
	Method    string
	Path      string
	Status    int
	Duration  time.Duration
	RequestID string
}

func (r *GormRepository) CreateMLflowDashboardTicket(ctx context.Context, record MLflowDashboardTicketRecord) error {
	record.TenantID = strings.TrimSpace(record.TenantID)
	record.UserID = strings.TrimSpace(record.UserID)
	if !mlflowDashboardTokenHashPattern.MatchString(record.TokenHash) {
		return fmt.Errorf("%w: token hash must be 64 lower-case hexadecimal characters", ErrMLflowDashboardTicketInvalid)
	}
	if record.TenantID == "" || record.UserID == "" {
		return fmt.Errorf("%w: tenant and user are required", ErrMLflowDashboardTicketInvalid)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	} else {
		record.CreatedAt = record.CreatedAt.UTC()
	}
	record.ExpiresAt = record.ExpiresAt.UTC()
	if !record.ExpiresAt.After(record.CreatedAt) {
		return fmt.Errorf("%w: expiry must be after creation", ErrMLflowDashboardTicketInvalid)
	}
	if record.ConsumedAt != nil {
		return fmt.Errorf("%w: new ticket cannot already be consumed", ErrMLflowDashboardTicketInvalid)
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create MLflow dashboard ticket: %w", err)
	}
	return nil
}

// ConsumeMLflowDashboardTicket performs one conditional write and returns the
// row changed by that statement. The consumed_at predicate makes the state
// transition atomic even when separate backend replicas race on the ticket.
func (r *GormRepository) ConsumeMLflowDashboardTicket(ctx context.Context, tokenHash string, now time.Time) (MLflowDashboardTicketRecord, error) {
	if !mlflowDashboardTokenHashPattern.MatchString(tokenHash) {
		return MLflowDashboardTicketRecord{}, ErrMLflowDashboardTicketInvalid
	}
	now = now.UTC()
	var consumed MLflowDashboardTicketRecord
	result := r.db.WithContext(ctx).
		Model(&consumed).
		Clauses(clause.Returning{}).
		Where("token_hash = ? AND consumed_at IS NULL AND expires_at > ?", tokenHash, now).
		Update("consumed_at", now)
	if result.Error != nil {
		return MLflowDashboardTicketRecord{}, fmt.Errorf("consume MLflow dashboard ticket: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return MLflowDashboardTicketRecord{}, ErrMLflowDashboardTicketInvalid
	}
	return consumed, nil
}

func (r *GormRepository) CreateMLflowAuditLog(ctx context.Context, event MLflowAuditEvent) error {
	normalizedPath := normalizeMLflowAuditPath(event.Path)
	durationMilliseconds := event.Duration.Milliseconds()
	if durationMilliseconds < 0 {
		durationMilliseconds = 0
	}
	outcome := "success"
	if event.Status >= 400 {
		outcome = "failure"
	}
	payload, err := json.Marshal(map[string]any{
		"actor_username": truncateMLflowAuditText(strings.TrimSpace(event.Principal.Username), mlflowAuditUsernameMaxLength),
		"auth_type":      truncateMLflowAuditText(strings.TrimSpace(string(event.Principal.AuthType)), mlflowAuditAuthTypeMaxLength),
		"outcome":        outcome,
		"method":         normalizeMLflowAuditMethod(event.Method),
		"path":           normalizedPath,
		"status":         event.Status,
		"duration_ms":    durationMilliseconds,
	})
	if err != nil {
		return fmt.Errorf("marshal MLflow audit payload: %w", err)
	}
	record := AuditLogRecord{
		TenantID:     truncateMLflowAuditText(strings.TrimSpace(event.Principal.TenantID), mlflowAuditIdentityMaxLength),
		UserID:       truncateMLflowAuditText(strings.TrimSpace(event.Principal.Subject), mlflowAuditIdentityMaxLength),
		Action:       "mlflow.dashboard.proxy",
		ResourceType: "mlflow",
		ResourceID:   normalizedPath,
		RequestID:    truncateMLflowAuditText(strings.TrimSpace(event.RequestID), mlflowAuditRequestIDMaxLength),
		PayloadJSON:  string(payload),
		CreatedAt:    time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create MLflow audit log: %w", err)
	}
	return nil
}

func normalizeMLflowAuditMethod(method string) string {
	method = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, strings.TrimSpace(method))
	return truncateMLflowAuditText(strings.ToUpper(method), mlflowAuditMethodMaxLength)
}

func normalizeMLflowAuditPath(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		if character == '\\' {
			return '/'
		}
		return character
	}, value)
	value = path.Clean("/" + strings.TrimLeft(value, "/"))
	if value == "." || value == "" {
		value = "/"
	}
	return truncateMLflowAuditText(value, mlflowAuditPathMaxLength)
}

func truncateMLflowAuditText(value string, limit int) string {
	if limit < 1 || len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
