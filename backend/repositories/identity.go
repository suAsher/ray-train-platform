package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func defaultTenantGPUQuota() int {
	quota := domain.CurrentResourceLimits().MaxTotalGPUs
	if quota < 1 {
		return 1
	}
	return quota
}

type TenantRecord struct {
	ID             string `gorm:"primaryKey"`
	Name           string
	Namespace      string `gorm:"uniqueIndex"`
	LocalQueue     string
	GPUQuotaLimit  int
	CPUQuotaMillis int64 `gorm:"column:cpu_quota_millis"`
	MemoryBytes    int64 `gorm:"column:memory_quota_bytes"`
	MaxPriority    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type UserRecord struct {
	ID          string `gorm:"primaryKey"`
	OIDCSubject string `gorm:"column:oidc_subject;uniqueIndex"`
	Username    string
	Email       string
	TenantID    string `gorm:"index"`
	RolesJSON   string `gorm:"column:roles;type:jsonb"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (TenantRecord) TableName() string { return "tenants" }
func (UserRecord) TableName() string   { return "users" }

func (r *GormRepository) EnsureIdentity(ctx context.Context, principal auth.Principal) error {
	if strings.TrimSpace(principal.Subject) == "" || strings.TrimSpace(principal.TenantID) == "" {
		return fmt.Errorf("authenticated subject and tenant are required")
	}
	namespace := "tenant-" + sanitizeDNS(principal.TenantID)
	queue := sanitizeDNS(principal.TenantID) + "-gpu"
	roles, err := json.Marshal(principal.Roles)
	if err != nil {
		return fmt.Errorf("marshal principal roles: %w", err)
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tenant := TenantRecord{ID: principal.TenantID, Name: principal.TenantID, Namespace: namespace, LocalQueue: queue, GPUQuotaLimit: defaultTenantGPUQuota(), MaxPriority: "normal", CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.Assignments(map[string]any{"name": tenant.Name, "namespace": tenant.Namespace, "local_queue": tenant.LocalQueue, "updated_at": now})}).Create(&tenant).Error; err != nil {
			return fmt.Errorf("upsert tenant: %w", err)
		}
		user := UserRecord{ID: principal.Subject, OIDCSubject: principal.Subject, Username: principal.Username, Email: principal.Email, TenantID: principal.TenantID, RolesJSON: string(roles), CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "oidc_subject"}}, DoUpdates: clause.Assignments(map[string]any{"username": user.Username, "email": user.Email, "tenant_id": user.TenantID, "roles": user.RolesJSON, "updated_at": now})}).Create(&user).Error; err != nil {
			return fmt.Errorf("upsert user: %w", err)
		}
		return nil
	})
}

func sanitizeDNS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		result = "default"
	}
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}

// CreateTenant provisions a team row. It is separate from EnsureIdentity, which
// upserts a tenant implicitly on login; this is the explicit administrative
// path and refuses to overwrite an existing team.
func (r *GormRepository) CreateTenant(ctx context.Context, tenant domain.Tenant) error {
	if err := tenant.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	record := TenantRecord{
		ID: tenant.ID, Name: tenant.Name, Namespace: tenant.Namespace,
		LocalQueue: tenant.LocalQueue, GPUQuotaLimit: tenant.GPUQuotaLimit, MaxPriority: "normal",
		CreatedAt: now, UpdatedAt: now,
	}
	var existing TenantRecord
	err := r.db.WithContext(ctx).Where("id = ?", tenant.ID).First(&existing).Error
	if err == nil {
		return fmt.Errorf("tenant %q already exists", tenant.ID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check existing tenant: %w", err)
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create tenant: %w", err)
	}
	return nil
}
