package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ray-train-platform-backend/domain"
)

type TenantSummary struct {
	RetiredAt        *time.Time              `json:"retiredAt"`
	RetiredBy        string                  `json:"retiredBy"`
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Namespace        string                  `json:"namespace"`
	QueueName        string                  `json:"queueName"`
	GPUQuotaLimit    int                     `json:"gpuQuotaLimit"`
	GPUQuotaUsed     int                     `json:"gpuQuotaUsed"`
	ActiveJobsCount  int                     `json:"activeJobsCount"`
	QueuedJobsCount  int                     `json:"queuedJobsCount"`
	MaxPriority      string                  `json:"maxPriority"`
	AcceleratorClass domain.AcceleratorClass `json:"acceleratorClass"`
	CreatedAt        time.Time               `json:"createdAt"`
}

type UserSummary struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	TenantID string   `json:"tenantId"`
	Roles    []string `json:"roles"`
}

func (r *GormRepository) ListTenantSummaries(ctx context.Context) ([]TenantSummary, error) {
	database := r.db.WithContext(ctx)
	var tenants []TenantRecord
	if err := database.Order("created_at ASC").Find(&tenants).Error; err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	var jobs []JobRecord
	if err := database.Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("list jobs for tenant summaries: %w", err)
	}
	summaries := make([]TenantSummary, 0, len(tenants))
	for _, tenant := range tenants {
		used, err := reservedTenantGPUs(database, tenant.ID)
		if err != nil {
			return nil, fmt.Errorf("calculate tenant %q gpu usage: %w", tenant.ID, err)
		}
		summary := TenantSummary{ID: tenant.ID, Name: tenant.Name, Namespace: tenant.Namespace, QueueName: tenant.LocalQueue, GPUQuotaLimit: effectiveGPUQuota(tenant.GPUQuotaLimit), GPUQuotaUsed: used, MaxPriority: tenant.MaxPriority, AcceleratorClass: domain.AcceleratorClass(tenant.AcceleratorClass).Resolved(), CreatedAt: tenant.CreatedAt}
		summary.RetiredAt, summary.RetiredBy = tenant.RetiredAt, tenant.RetiredBy
		for _, job := range jobs {
			if job.TenantID != tenant.ID {
				continue
			}
			switch domain.State(job.ObservedState) {
			case domain.StateSubmitted, domain.StateValidating, domain.StateQueued, domain.StateAdmitted, domain.StateProvisioning, domain.StateRunning, domain.StateRecovering:
				summary.ActiveJobsCount++
			case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
			}
			if domain.State(job.ObservedState) == domain.StateQueued {
				summary.QueuedJobsCount++
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (r *GormRepository) TenantAcceleratorClass(ctx context.Context, tenantID string) (domain.AcceleratorClass, error) {
	var tenant TenantRecord
	if err := r.db.WithContext(ctx).Select("accelerator_class").Where("id = ? AND retired_at IS NULL", tenantID).First(&tenant).Error; err != nil {
		return "", fmt.Errorf("read tenant accelerator class: %w", err)
	}
	accelerator := domain.AcceleratorClass(tenant.AcceleratorClass).Resolved()
	if err := accelerator.Validate(); err != nil {
		return "", err
	}
	return accelerator, nil
}

func (r *GormRepository) SetTenantAcceleratorClass(ctx context.Context, tenantID string, accelerator domain.AcceleratorClass) error {
	accelerator = accelerator.Resolved()
	if err := accelerator.Validate(); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&TenantRecord{}).
		Where("id = ? AND retired_at IS NULL", tenantID).
		Updates(map[string]any{"accelerator_class": string(accelerator), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("update tenant accelerator class: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("tenant %q was not found", tenantID)
	}
	return nil
}

// SetTenantGPUQuota reallocates a team's GPU budget. The value is the same one
// enforceTenantGPUQuota checks on every submission, so the change takes effect
// on the next job without a redeploy.
func (r *GormRepository) SetTenantGPUQuota(ctx context.Context, tenantID string, limit int) error {
	if limit < 0 {
		return fmt.Errorf("tenant gpu quota cannot be negative")
	}
	result := r.db.WithContext(ctx).Model(&TenantRecord{}).
		Where("id = ?", tenantID).
		Updates(map[string]any{"gpu_quota_limit": limit, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("update tenant gpu quota: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("tenant %q was not found", tenantID)
	}
	return nil
}

func (r *GormRepository) ListUserSummaries(ctx context.Context) ([]UserSummary, error) {
	if r.db.Migrator().HasTable(&LocalUserRecord{}) && r.db.Migrator().HasTable(&TenantMembershipRecord{}) {
		users, err := r.ListLocalUsers(ctx)
		if err != nil {
			return nil, fmt.Errorf("list active platform users: %w", err)
		}
		items := make([]UserSummary, 0, len(users))
		for _, user := range users {
			items = append(items, UserSummary{
				ID: user.ID, Username: user.Username, Email: user.Email,
				TenantID: user.TenantID, Roles: append([]string(nil), user.Roles...),
			})
		}
		return items, nil
	}
	var users []UserRecord
	if err := r.db.WithContext(ctx).Order("created_at ASC").Find(&users).Error; err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	items := make([]UserSummary, 0, len(users))
	for _, user := range users {
		roles := make([]string, 0)
		_ = json.Unmarshal([]byte(user.RolesJSON), &roles)
		items = append(items, UserSummary{ID: user.ID, Username: user.Username, Email: user.Email, TenantID: user.TenantID, Roles: roles})
	}
	return items, nil
}
