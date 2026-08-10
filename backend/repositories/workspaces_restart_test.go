package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/domain"
)

func devWorkspace(id string) *domain.DevWorkspace {
	return &domain.DevWorkspace{
		ID: id, TenantID: "team-a", UserID: "user-1",
		Name: "dev-" + id, Namespace: "tenant-team-a", RayClusterName: "dev-" + id,
		GPUCount: 1, State: domain.WorkspaceSubmitted,
	}
}

// dev_workspaces has a unique constraint on (tenant_id, user_id). Stopping a
// workspace leaves the row behind, so a plain insert makes the second launch
// fail forever with a duplicate key error — the user can never restart their
// debug environment.
func TestCreateWorkspaceReplacesAStoppedOne(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()

	first := devWorkspace("ws-1")
	if err := repo.CreateWorkspace(ctx, first, 3600); err != nil {
		t.Fatalf("create first workspace: %v", err)
	}
	if err := repo.UpdateWorkspaceState(ctx, "team-a", "user-1", domain.WorkspaceStopped); err != nil {
		t.Fatalf("stop workspace: %v", err)
	}

	second := devWorkspace("ws-2")
	if err := repo.CreateWorkspace(ctx, second, 3600); err != nil {
		t.Fatalf("relaunching after a stop must succeed: %v", err)
	}

	current, err := repo.GetWorkspace(ctx, "team-a", "user-1")
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if current.ID != "ws-2" || current.State == domain.WorkspaceStopped {
		t.Fatalf("expected the new workspace to be current, got %+v", current)
	}
}

// A workspace that is still running must not be silently replaced: that would
// orphan its RayCluster and leak the GPU.
func TestCreateWorkspaceRefusesToReplaceARunningOne(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()

	if err := repo.CreateWorkspace(ctx, devWorkspace("ws-1"), 3600); err != nil {
		t.Fatalf("create first workspace: %v", err)
	}
	if err := repo.UpdateWorkspaceState(ctx, "team-a", "user-1", domain.WorkspaceRunning); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	if err := repo.CreateWorkspace(ctx, devWorkspace("ws-2"), 3600); err == nil {
		t.Fatalf("expected a running workspace to block a second launch")
	}
}
