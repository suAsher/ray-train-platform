package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ray-train-platform-backend/domain"
)

type TenantSummary struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Namespace       string    `json:"namespace"`
	QueueName       string    `json:"queueName"`
	GPUQuotaLimit   int       `json:"gpuQuotaLimit"`
	GPUQuotaUsed    int       `json:"gpuQuotaUsed"`
	ActiveJobsCount int       `json:"activeJobsCount"`
	QueuedJobsCount int       `json:"queuedJobsCount"`
	MaxPriority     string    `json:"maxPriority"`
	CreatedAt       time.Time `json:"createdAt"`
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
		summary := TenantSummary{ID: tenant.ID, Name: tenant.Name, Namespace: tenant.Namespace, QueueName: tenant.LocalQueue, GPUQuotaLimit: effectiveGPUQuota(tenant.GPUQuotaLimit), GPUQuotaUsed: used, MaxPriority: tenant.MaxPriority, CreatedAt: tenant.CreatedAt}
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
