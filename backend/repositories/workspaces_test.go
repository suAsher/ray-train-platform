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

func TestAdministrativeWorkspaceLookupAndUpdateRemainIDScoped(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	workspace := &domain.DevWorkspace{ID: "ws-original", TenantID: "tenant-a", UserID: "owner", Name: "debug", Namespace: "tenant-a", RayClusterName: "debug", State: domain.WorkspaceSubmitted}
	if err := repo.CreateWorkspace(ctx, workspace, 3600); err != nil {
		t.Fatal(err)
	}
	store, ok := any(repo).(interface {
		GetWorkspaceByID(context.Context, string, string) (*domain.DevWorkspace, error)
		UpdateWorkspaceStateByID(context.Context, string, domain.WorkspaceState) error
	})
	if !ok {
		t.Fatal("administrative workspace lookup and ID-scoped updates are missing")
	}
	for _, tenant := range []string{"", "tenant-a"} {
		got, err := store.GetWorkspaceByID(ctx, "ws-original", tenant)
		if err != nil || got.UserID != "owner" {
			t.Fatalf("lookup tenant=%q got=%+v err=%v", tenant, got, err)
		}
	}
	if _, err := store.GetWorkspaceByID(ctx, "ws-original", "tenant-b"); err == nil {
		t.Fatal("cross-tenant lookup must fail")
	}
	if err := store.UpdateWorkspaceStateByID(ctx, "ws-original", domain.WorkspaceStopped); err != nil {
		t.Fatal(err)
	}
	replacement := *workspace
	replacement.ID = "ws-replacement"
	if err := repo.CreateWorkspace(ctx, &replacement, 3600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkspaceStateByID(ctx, "ws-original", domain.WorkspaceStopped); err == nil {
		t.Fatal("stale target update must fail")
	}
	got, err := repo.GetWorkspace(ctx, "tenant-a", "owner")
	if err != nil || got.ID != "ws-replacement" || got.State != domain.WorkspaceSubmitted {
		t.Fatalf("replacement workspace changed: %+v err=%v", got, err)
	}
}
