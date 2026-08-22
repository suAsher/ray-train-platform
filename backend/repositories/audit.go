package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ray-train-platform-backend/auth"
)

// AuditLogRecord maps the pre-existing audit_logs table. The payload is a
// deliberately small allowlist of metadata; callers cannot provide arbitrary
// request bodies, which prevents passwords and tokens from reaching audit.
type AuditLogRecord struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"column:tenant_id"`
	UserID       string    `gorm:"column:user_id"`
	Action       string    `gorm:"column:action"`
	ResourceType string    `gorm:"column:resource_type"`
	ResourceID   string    `gorm:"column:resource_id"`
	RequestID    string    `gorm:"column:request_id"`
	PayloadJSON  string    `gorm:"column:payload;type:jsonb"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (AuditLogRecord) TableName() string { return "audit_logs" }

func (r *GormRepository) CreateAuditLog(ctx context.Context, action, resourceID string, actor auth.Principal, requestID string) error {
	payload, err := json.Marshal(map[string]string{
		"actor_username": actor.Username,
		"auth_type":      string(actor.AuthType),
		"outcome":        "success",
	})
	if err != nil {
		return fmt.Errorf("marshal local account audit payload: %w", err)
	}
	record := AuditLogRecord{
		TenantID: actor.TenantID, UserID: actor.Subject, Action: action,
		ResourceType: "local_user", ResourceID: resourceID, RequestID: requestID,
		PayloadJSON: string(payload), CreatedAt: time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create local account audit log: %w", err)
	}
	return nil
}
