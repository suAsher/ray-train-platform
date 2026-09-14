package repositories

import (
	"context"
	"errors"
	"testing"

	"ray-train-platform-backend/domain"
)

type workspaceLifecycleContract interface {
	BeginWorkspaceStop(context.Context, string, string) (*domain.DevWorkspace, error)
	WithWorkspaceOperation(context.Context, string, string, func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error) error
	ApplyWorkspaceObservation(context.Context, string, domain.WorkspaceState, domain.WorkspaceState) (bool, error)
}

func lifecycleContract(t *testing.T, repo *GormRepository) workspaceLifecycleContract {
	t.Helper()
	store, ok := any(repo).(workspaceLifecycleContract)
	if !ok {
		t.Fatal("workspace lifecycle locking and conditional transitions are missing")
	}
	return store
}

func TestWorkspaceLifecycleStopIntentSurvivesCleanupRollback(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	ws := &domain.DevWorkspace{ID: "ws-life", TenantID: "tenant-a", UserID: "owner", Namespace: "tenant-a", RayClusterName: "dev-life", State: domain.WorkspaceSubmitted, GPUCount: 2}
	if err := repo.CreateWorkspace(ctx, ws, 3600); err != nil {
		t.Fatal(err)
	}
	store := lifecycleContract(t, repo)
	if _, err := store.BeginWorkspaceStop(ctx, ws.ID, "tenant-other"); err == nil {
		t.Fatal("cross-team stop accepted")
	}
	stopping, err := store.BeginWorkspaceStop(ctx, ws.ID, "tenant-a")
	if err != nil || stopping.State != domain.WorkspaceStopping {
		t.Fatalf("stop intent: %+v %v", stopping, err)
	}
	err = store.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(locked *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		if locked.State != domain.WorkspaceStopping {
			t.Fatal("stop intent was not persisted")
		}
		if err := setState(domain.WorkspaceStopped); err != nil {
			return err
		}
		return errors.New("cleanup transaction failed")
	})
	if err == nil {
		t.Fatal("expected callback rollback")
	}
	got, err := repo.GetWorkspaceByID(ctx, ws.ID, "tenant-a")
	if err != nil || got.State != domain.WorkspaceStopping {
		t.Fatalf("lost stop intent after failure: %+v %v", got, err)
	}
	if used, err := reservedTenantGPUs(repo.db, "tenant-a"); err != nil || used != 2 {
		t.Fatalf("stopping quota released: %d %v", used, err)
	}
	for _, previous := range []domain.WorkspaceState{domain.WorkspaceSubmitted, domain.WorkspaceRunning, domain.WorkspaceStopping} {
		changed, err := store.ApplyWorkspaceObservation(ctx, ws.ID, previous, domain.WorkspaceRunning)
		if err != nil || changed {
			t.Fatalf("observation erased STOPPING: %v %v", changed, err)
		}
	}
	if err := store.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(_ *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		return setState(domain.WorkspaceStopped)
	}); err != nil {
		t.Fatal(err)
	}
	if used, err := reservedTenantGPUs(repo.db, "tenant-a"); err != nil || used != 0 {
		t.Fatalf("completed stop quota: %d %v", used, err)
	}
	if got, err := store.BeginWorkspaceStop(ctx, ws.ID, "tenant-a"); err != nil || got.State != domain.WorkspaceStopped {
		t.Fatalf("idempotent stop changed terminal state: %+v %v", got, err)
	}
}

func TestWorkspaceLifecycleObservationCannotChangeReplacementOrTerminal(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	ws := &domain.DevWorkspace{ID: "ws-old", TenantID: "tenant-a", UserID: "owner", Namespace: "tenant-a", RayClusterName: "dev-old", State: domain.WorkspaceSubmitted}
	if err := repo.CreateWorkspace(ctx, ws, 3600); err != nil {
		t.Fatal(err)
	}
	store := lifecycleContract(t, repo)
	if changed, err := store.ApplyWorkspaceObservation(ctx, ws.ID, domain.WorkspaceSubmitted, domain.WorkspaceRunning); err != nil || !changed {
		t.Fatalf("active observation failed: %v %v", changed, err)
	}
	if changed, err := store.ApplyWorkspaceObservation(ctx, ws.ID, domain.WorkspaceSubmitted, domain.WorkspaceFailed); err != nil || changed {
		t.Fatalf("stale observation accepted: %v %v", changed, err)
	}
	if _, err := store.BeginWorkspaceStop(ctx, ws.ID, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(_ *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		return setState(domain.WorkspaceStopped)
	}); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.ApplyWorkspaceObservation(ctx, ws.ID, domain.WorkspaceStopped, domain.WorkspaceRunning); err != nil || changed {
		t.Fatalf("terminal observation accepted: %v %v", changed, err)
	}
	replacement := *ws
	replacement.ID = "ws-new"
	if err := repo.CreateWorkspace(ctx, &replacement, 3600); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.ApplyWorkspaceObservation(ctx, ws.ID, domain.WorkspaceSubmitted, domain.WorkspaceFailed); err != nil || changed {
		t.Fatalf("replacement changed through stale ID: %v %v", changed, err)
	}
	if err := store.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error {
		t.Fatal("stale callback executed")
		return nil
	}); err == nil {
		t.Fatal("missing target accepted")
	}
}
