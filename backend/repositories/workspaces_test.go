package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestWorkspaceIsTenantScoped(t *testing.T) {
	repo := testRepository(t)
	workspace := &domain.DevWorkspace{ID: "ws-1", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a", RayClusterName: "debug-a", State: domain.WorkspaceSubmitted, GPUCount: 1}
	if err := repo.CreateWorkspace(context.Background(), workspace, 3600); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := repo.GetWorkspace(context.Background(), "tenant-b", "user-a"); err == nil {
		t.Fatal("expected tenant isolation")
	}
	got, err := repo.GetWorkspace(context.Background(), "tenant-a", "user-a")
	if err != nil || got.JupyterURL == "" {
		t.Fatalf("unexpected workspace: %+v err=%v", got, err)
	}
}
