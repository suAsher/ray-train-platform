package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

type WorkspaceRecord struct {
	ID        string `gorm:"primaryKey"`
	TenantID  string `gorm:"index"`
	UserID    string `gorm:"index"`
	Name      string
	Namespace string
	// The migration created this column as raycluster_name; GORM would
	// otherwise derive ray_cluster_name and fail on PostgreSQL.
	RayClusterName string `gorm:"column:raycluster_name"`
	JupyterService string
	ObservedState  string
	GPUCount       int
	IdleTTLSeconds int64
	ExpiresAt      *time.Time
	SnapshotID     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (WorkspaceRecord) TableName() string { return "dev_workspaces" }

func (r *GormRepository) CreateWorkspace(ctx context.Context, workspace *domain.DevWorkspace, idleTTLSeconds int64) error {
	if workspace == nil || workspace.ID == "" || workspace.TenantID == "" || workspace.UserID == "" {
		return fmt.Errorf("workspace identity is required")
	}
	record := WorkspaceRecord{ID: workspace.ID, TenantID: workspace.TenantID, UserID: workspace.UserID, Name: workspace.Name, Namespace: workspace.Namespace, RayClusterName: workspace.RayClusterName, JupyterService: workspace.RayClusterName + "-head-svc", ObservedState: string(workspace.State), GPUCount: workspace.GPUCount, IdleTTLSeconds: idleTTLSeconds, ExpiresAt: workspace.ExpiresAt, SnapshotID: workspace.SnapshotID}
	// dev_workspaces is unique per (tenant, user). A stopped or failed row is
	// spent and must give way to the new launch, otherwise a user who stops
	// their environment can never start another one. A live one is kept:
	// replacing it would orphan its RayCluster and leak the GPU.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing WorkspaceRecord
		err := tx.Where("tenant_id = ? AND user_id = ?", workspace.TenantID, workspace.UserID).First(&existing).Error
		switch {
		case err == nil:
			state := domain.WorkspaceState(existing.ObservedState)
			if state != domain.WorkspaceStopped && state != domain.WorkspaceFailed {
				return fmt.Errorf("a workspace is already active for this user")
			}
			if err := tx.Where("id = ?", existing.ID).Delete(&WorkspaceRecord{}).Error; err != nil {
				return fmt.Errorf("clear stopped workspace: %w", err)
			}
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return fmt.Errorf("look up existing workspace: %w", err)
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		return nil
	})
}

func (r *GormRepository) GetWorkspace(ctx context.Context, tenantID, userID string) (*domain.DevWorkspace, error) {
	var record WorkspaceRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", tenantID, userID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("workspace not found")
		}
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	return record.toDomain(), nil
}

// GetWorkspaceByUser looks a workspace up by owner alone. The JupyterLab proxy
// authorises with a workspace-scoped token that carries the user but not the
// tenant, and the tenant is implied by the owner.
func (r *GormRepository) GetWorkspaceByUser(ctx context.Context, userID string) (*domain.DevWorkspace, error) {
	var record WorkspaceRecord
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").First(&record).Error; err != nil {
		return nil, fmt.Errorf("get workspace by user: %w", err)
	}
	return record.toDomain(), nil
}

func (r *GormRepository) UpdateWorkspaceState(ctx context.Context, tenantID, userID string, state domain.WorkspaceState) error {
	result := r.db.WithContext(ctx).Model(&WorkspaceRecord{}).Where("tenant_id = ? AND user_id = ?", tenantID, userID).Updates(map[string]any{"observed_state": state, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("update workspace state: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("workspace not found")
	}
	return nil
}

func (r WorkspaceRecord) toDomain() *domain.DevWorkspace {
	return &domain.DevWorkspace{ID: r.ID, TenantID: r.TenantID, UserID: r.UserID, Name: r.Name, Namespace: r.Namespace, RayClusterName: r.RayClusterName, JupyterURL: "/api/v1/dev-workspaces/" + r.ID + "/proxy/", SnapshotID: r.SnapshotID, GPUCount: r.GPUCount, State: domain.WorkspaceState(r.ObservedState), ExpiresAt: r.ExpiresAt}
}
