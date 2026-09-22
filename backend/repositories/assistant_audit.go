package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ray-train-platform-backend/auth"
)

// CreateAssistantAuditLog records intent before a model request; it never labels
// that intent as successful inference and never stores prompts or log contents.
func (r *GormRepository) CreateAssistantAuditLog(ctx context.Context, actor auth.Principal, requestID, mode string, includesLogs bool) error {
	if actor.Subject == "" || actor.TenantID == "" || requestID == "" {
		return fmt.Errorf("assistant audit requires authenticated attribution")
	}
	switch mode {
	case "auto", "api", "local", "docs":
	default:
		return fmt.Errorf("unsupported assistant audit mode")
	}
	payload, err := json.Marshal(struct {
		Mode         string `json:"mode"`
		IncludesLogs bool   `json:"includesLogs"`
		Outcome      string `json:"outcome"`
	}{mode, includesLogs, "started"})
	if err != nil {
		return fmt.Errorf("encode assistant audit: %w", err)
	}
	record := AuditLogRecord{TenantID: actor.TenantID, UserID: actor.Subject, Action: "assistant.query.started", ResourceType: "assistant_query", ResourceID: requestID, RequestID: requestID, PayloadJSON: string(payload), CreatedAt: time.Now().UTC()}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create assistant audit: %w", err)
	}
	return nil
}
