package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ray-train-platform-backend/auth"
)

const administrativeAuditTextLimit = 128

// AdministrativeAuditEvent is intentionally narrow. Only the two supported
// team-governance actions and their allowlisted metadata can reach audit_logs.
type AdministrativeAuditEvent struct {
	Action         string
	ResourceID     string
	SourceTenantID string
	TargetTenantID string
	NewName        string
	Principal      auth.Principal
	RequestID      string
}

func (r *GormRepository) CreateAdministrativeAuditLog(ctx context.Context, event AdministrativeAuditEvent) error {
	resourceType := ""
	switch strings.TrimSpace(event.Action) {
	case "tenant.renamed":
		resourceType = "tenant"
	case "tenant_membership.reassigned":
		resourceType = "local_user"
	default:
		return fmt.Errorf("unsupported administrative audit action")
	}
	payload, err := json.Marshal(map[string]string{
		"actor_username": truncateAdministrativeAuditText(event.Principal.Username),
		"auth_type":      truncateAdministrativeAuditText(string(event.Principal.AuthType)),
		"source_tenant":  truncateAdministrativeAuditText(event.SourceTenantID),
		"target_tenant":  truncateAdministrativeAuditText(event.TargetTenantID),
		"new_name":       truncateAdministrativeAuditText(event.NewName),
		"outcome":        "success",
	})
	if err != nil {
		return fmt.Errorf("marshal administrative audit payload: %w", err)
	}
	record := AuditLogRecord{
		TenantID:     truncateAdministrativeAuditText(event.Principal.TenantID),
		UserID:       truncateAdministrativeAuditText(event.Principal.Subject),
		Action:       strings.TrimSpace(event.Action),
		ResourceType: resourceType,
		ResourceID:   truncateAdministrativeAuditText(event.ResourceID),
		RequestID:    truncateAdministrativeAuditText(event.RequestID),
		PayloadJSON:  string(payload),
		CreatedAt:    time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create administrative audit log: %w", err)
	}
	return nil
}

func truncateAdministrativeAuditText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > administrativeAuditTextLimit {
		runes = runes[:administrativeAuditTextLimit]
	}
	return string(runes)
}
