package repositories

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func workspacePostgresStores(t *testing.T) (*GormRepository, *GormRepository) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("workspace_lifecycle_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	scopedDSN := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		scopedDSN = parsed.String()
	}
	a, b := openArtifactPostgresConnection(t, scopedDSN), openArtifactPostgresConnection(t, scopedDSN)
	for _, database := range []*gorm.DB{a, b} {
		if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := a.AutoMigrate(&WorkspaceRecord{}, &TenantRecord{}, &JobRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Create(&TenantRecord{ID: "tenant-a", GPUQuotaLimit: 8}).Error; err != nil {
		t.Fatal(err)
	}
	return NewGormRepository(a), NewGormRepository(b)
}

func TestWorkspaceLifecyclePostgresSerializesCreatorAndStopAcrossConnections(t *testing.T) {
	a, b := workspacePostgresStores(t)
	first, second := lifecycleContract(t, a), lifecycleContract(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := &domain.DevWorkspace{ID: "ws-pg", TenantID: "tenant-a", UserID: "owner", Namespace: "tenant-a", RayClusterName: "dev-pg", State: domain.WorkspaceSubmitted, GPUCount: 1}
	if err := a.CreateWorkspace(ctx, ws, 3600); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	creator := make(chan error, 1)
	go func() {
		creator <- first.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(_ *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
			close(entered)
			select {
			case <-release:
				return setState(domain.WorkspaceRunning)
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("creator did not acquire lock")
	}
	stop := make(chan error, 1)
	go func() { _, err := second.BeginWorkspaceStop(ctx, ws.ID, "tenant-a"); stop <- err }()
	select {
	case err := <-stop:
		close(release)
		t.Fatalf("stop bypassed creator's row lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-creator; err != nil {
		t.Fatal(err)
	}
	if err := <-stop; err != nil {
		t.Fatal(err)
	}
	if err := second.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(locked *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		if locked.State != domain.WorkspaceStopping {
			return fmt.Errorf("state=%s", locked.State)
		}
		return setState(domain.WorkspaceStopped)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetWorkspaceByID(ctx, ws.ID, "tenant-a")
	if err != nil || got.State != domain.WorkspaceStopped {
		t.Fatalf("late creator revived workspace: %+v %v", got, err)
	}
}

func TestWorkspaceLifecyclePostgresLockCancellationRollbackAndRetry(t *testing.T) {
	a, b := workspacePostgresStores(t)
	first, second := lifecycleContract(t, a), lifecycleContract(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := &domain.DevWorkspace{ID: "ws-pg", TenantID: "tenant-a", UserID: "owner", Namespace: "tenant-a", RayClusterName: "dev-pg", State: domain.WorkspaceSubmitted, GPUCount: 1}
	if err := a.CreateWorkspace(ctx, ws, 3600); err != nil {
		t.Fatal(err)
	}
	if _, err := first.BeginWorkspaceStop(ctx, ws.ID, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- first.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(_ *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
			if err := setState(domain.WorkspaceStopped); err != nil {
				return err
			}
			close(entered)
			select {
			case <-release:
				return errors.New("forced rollback")
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("holder did not acquire lock")
	}
	blocked, stopWait := context.WithTimeout(ctx, 150*time.Millisecond)
	err := second.WithWorkspaceOperation(blocked, ws.ID, "tenant-a", func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error {
		t.Error("canceled waiter entered callback")
		return nil
	})
	stopWait()
	if err == nil {
		close(release)
		t.Fatal("lock ignored context cancellation")
	}
	close(release)
	if err := <-holder; err == nil {
		t.Fatal("callback rollback lost")
	}
	got, err := b.GetWorkspaceByID(ctx, ws.ID, "tenant-a")
	if err != nil || got.State != domain.WorkspaceStopping {
		t.Fatalf("rollback lost persisted STOPPING: %+v %v", got, err)
	}
	if err := second.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(_ *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
		return setState(domain.WorkspaceStopped)
	}); err != nil {
		t.Fatalf("retry after canceled wait failed: %v", err)
	}
}

func TestWorkspaceLifecyclePostgresRelaunchRechecksStateAfterWaiting(t *testing.T) {
	a, b := workspacePostgresStores(t)
	first := lifecycleContract(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := &domain.DevWorkspace{ID: "ws-old", TenantID: "tenant-a", UserID: "owner", Namespace: "tenant-a", RayClusterName: "dev-old", State: domain.WorkspaceFailed, GPUCount: 1}
	if err := a.CreateWorkspace(ctx, ws, 3600); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- first.WithWorkspaceOperation(ctx, ws.ID, "tenant-a", func(_ *domain.DevWorkspace, setState func(domain.WorkspaceState) error) error {
			if err := setState(domain.WorkspaceStopping); err != nil {
				return err
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("holder did not acquire lock")
	}
	replacement := *ws
	replacement.ID = "ws-new"
	replacement.State = domain.WorkspaceSubmitted
	created := make(chan error, 1)
	go func() { created <- b.CreateWorkspace(ctx, &replacement, 3600) }()
	select {
	case err := <-created:
		close(release)
		t.Fatalf("relaunch bypassed lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-holder; err != nil {
		t.Fatal(err)
	}
	if err := <-created; err == nil {
		t.Fatal("relaunch deleted a workspace that changed to STOPPING while waiting")
	}
	got, err := a.GetWorkspace(ctx, "tenant-a", "owner")
	if err != nil || got.ID != "ws-old" || got.State != domain.WorkspaceStopping {
		t.Fatalf("relaunch replaced cleanup target: %+v %v", got, err)
	}
}
