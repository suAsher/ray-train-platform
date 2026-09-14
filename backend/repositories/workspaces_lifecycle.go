package repositories

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

// WithWorkspaceOperation serializes runtime side effects across backend
// replicas. The workspace must already have been committed by CreateWorkspace,
// so a crash/rollback cannot erase the identity of resources created here.
func (r *GormRepository) WithWorkspaceOperation(ctx context.Context, id, tenantID string, operation func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id)
		if tenantID != "" {
			query = query.Where("tenant_id = ?", tenantID)
		}
		var record WorkspaceRecord
		if err := query.First(&record).Error; err != nil {
			return fmt.Errorf("lock workspace operation: %w", err)
		}
		transactionStore := NewGormRepository(tx)
		return operation(record.toDomain(), func(state domain.WorkspaceState) error {
			return transactionStore.UpdateWorkspaceStateByID(ctx, record.ID, state)
		})
	})
}

// BeginWorkspaceStop commits intent before cleanup starts. Cleanup rollback or
// a canceled request leaves STOPPING reserved and available for another retry.
func (r *GormRepository) BeginWorkspaceStop(ctx context.Context, id, tenantID string) (*domain.DevWorkspace, error) {
	var result *domain.DevWorkspace
	err := r.WithWorkspaceOperation(ctx, id, tenantID, func(workspace *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		copy := *workspace
		if workspace.State != domain.WorkspaceStopped {
			if err := setState(domain.WorkspaceStopping); err != nil {
				return err
			}
			copy.State = domain.WorkspaceStopping
		}
		result = &copy
		return nil
	})
	return result, err
}

// ApplyWorkspaceObservation is only for active runtime observations, never for
// stop intent. A stale poll cannot revive STOPPING/STOPPED or a replacement ID.
func (r *GormRepository) ApplyWorkspaceObservation(ctx context.Context, id string, previous, next domain.WorkspaceState) (bool, error) {
	if (previous != domain.WorkspaceSubmitted && previous != domain.WorkspaceRunning) ||
		(next != domain.WorkspaceSubmitted && next != domain.WorkspaceRunning && next != domain.WorkspaceFailed) {
		return false, nil
	}
	result := r.db.WithContext(ctx).Model(&WorkspaceRecord{}).Where("id = ? AND observed_state = ?", id, previous).Updates(map[string]any{"observed_state": next, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, fmt.Errorf("observe workspace state: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}
