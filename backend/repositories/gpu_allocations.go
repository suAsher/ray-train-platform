package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"ray-train-platform-backend/domain"
)

var activeGPUAllocationJobStates = []string{
	string(domain.StateSubmitted),
	string(domain.StateValidating),
	string(domain.StateQueued),
	string(domain.StateAdmitted),
	string(domain.StateProvisioning),
	string(domain.StateRunning),
	string(domain.StateRecovering),
	string(domain.StateCanceling),
	string(domain.StateDeleting),
	string(domain.StateUnknown),
}

var activeGPUAllocationWorkspaceStates = []string{
	string(domain.WorkspaceSubmitted),
	string(domain.WorkspaceRunning),
	string(domain.WorkspaceStopping),
}

// ListGPUAllocations returns active training and interactive workloads. Tenant
// scoping is applied independently to both workload queries so a caller cannot
// cross a tenant boundary through either resource type.
func (r *GormRepository) ListGPUAllocations(ctx context.Context, tenantID string, allTenants bool) ([]domain.GPUAllocation, error) {
	jobQuery := r.db.WithContext(ctx).
		Where("observed_state IN ?", activeGPUAllocationJobStates).
		Where("archived_at IS NULL")
	workspaceQuery := r.db.WithContext(ctx).
		Where("observed_state IN ?", activeGPUAllocationWorkspaceStates)
	if !allTenants {
		jobQuery = jobQuery.Where("tenant_id = ?", tenantID)
		workspaceQuery = workspaceQuery.Where("tenant_id = ?", tenantID)
	}

	var jobs []JobRecord
	if err := jobQuery.Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("list active training jobs: %w", err)
	}
	var workspaces []WorkspaceRecord
	if err := workspaceQuery.Find(&workspaces).Error; err != nil {
		return nil, fmt.Errorf("list active debug workspaces: %w", err)
	}

	allocations := make([]domain.GPUAllocation, 0, len(jobs)+len(workspaces))
	userIDs := make(map[string]struct{}, len(jobs)+len(workspaces))
	for _, record := range jobs {
		var spec domain.JobSpec
		if err := json.Unmarshal([]byte(record.SpecJSON), &spec); err != nil {
			return nil, fmt.Errorf("decode active training job %q spec: %w", record.ID, err)
		}
		allocations = append(allocations, domain.GPUAllocation{
			ID:           record.ID,
			Type:         domain.GPUAllocationTrainingJob,
			Name:         record.Name,
			TenantID:     record.TenantID,
			UserID:       record.UserID,
			State:        record.ObservedState,
			GPUCount:     spec.Resources.WorkerReplicas * spec.Resources.GPUsPerWorker,
			Namespace:    record.KubernetesNS,
			ResourceName: record.RayJobName,
			CreatedAt:    record.CreatedAt,
			StartedAt:    record.StartedAt,
		})
		userIDs[record.UserID] = struct{}{}
	}
	for _, record := range workspaces {
		allocations = append(allocations, domain.GPUAllocation{
			ID:           record.ID,
			Type:         domain.GPUAllocationDebugWorkspace,
			Name:         record.Name,
			TenantID:     record.TenantID,
			UserID:       record.UserID,
			State:        record.ObservedState,
			GPUCount:     record.GPUCount,
			Namespace:    record.Namespace,
			ResourceName: record.RayClusterName,
			CreatedAt:    record.CreatedAt,
		})
		userIDs[record.UserID] = struct{}{}
	}

	usernames, err := r.gpuAllocationUsernames(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for index := range allocations {
		allocation := &allocations[index]
		allocation.Username = usernames[allocation.UserID]
		if allocation.Username == "" {
			allocation.Username = allocation.UserID
		}
	}
	sort.Slice(allocations, func(left, right int) bool {
		if allocations[left].CreatedAt.Equal(allocations[right].CreatedAt) {
			return allocations[left].ID < allocations[right].ID
		}
		return allocations[left].CreatedAt.Before(allocations[right].CreatedAt)
	})
	return allocations, nil
}

func (r *GormRepository) gpuAllocationUsernames(ctx context.Context, userIDs map[string]struct{}) (map[string]string, error) {
	usernames := make(map[string]string, len(userIDs))
	if len(userIDs) == 0 {
		return usernames, nil
	}
	ids := make([]string, 0, len(userIDs))
	for id := range userIDs {
		ids = append(ids, id)
	}
	var users []UserRecord
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("list GPU allocation users: %w", err)
	}
	for _, user := range users {
		usernames[user.ID] = user.Username
	}
	return usernames, nil
}
