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
	var tenants []TenantRecord
	if err := r.db.WithContext(ctx).Order("created_at ASC").Find(&tenants).Error; err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	var jobs []JobRecord
	if err := r.db.WithContext(ctx).Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("list jobs for tenant summaries: %w", err)
	}
	summaries := make([]TenantSummary, 0, len(tenants))
	for _, tenant := range tenants {
		quota := tenant.GPUQuotaLimit
		if quota <= 0 {
			quota = defaultTenantGPUQuota
		}
		summary := TenantSummary{ID: tenant.ID, Name: tenant.Name, Namespace: tenant.Namespace, QueueName: tenant.LocalQueue, GPUQuotaLimit: quota, MaxPriority: tenant.MaxPriority, CreatedAt: tenant.CreatedAt}
		for _, job := range jobs {
			if job.TenantID != tenant.ID {
				continue
			}
			switch domain.State(job.ObservedState) {
			case domain.StateSubmitted, domain.StateValidating, domain.StateQueued, domain.StateAdmitted, domain.StateProvisioning, domain.StateRunning:
				summary.ActiveJobsCount++
				var spec domain.JobSpec
				if json.Unmarshal([]byte(job.SpecJSON), &spec) == nil {
					summary.GPUQuotaUsed += spec.Resources.WorkerReplicas * spec.Resources.GPUsPerWorker
				}
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
