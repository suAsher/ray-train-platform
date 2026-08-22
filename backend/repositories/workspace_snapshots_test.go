package repositories

import (
	"context"
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func TestWorkspaceSnapshotsAreOwnerScoped(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&WorkspaceSnapshotRecord{}); err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	snapshot := domain.WorkspaceSnapshot{ID: "snapshot-a", TenantID: "team-a", UserID: "user-a", SourcePath: "project", FileCount: 2}
	if err := repository.CreateWorkspaceSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetWorkspaceSnapshot(context.Background(), "team-a", "user-b", "snapshot-a"); !errors.Is(err, ErrWorkspaceSnapshotNotFound) {
		t.Fatalf("other user lookup err=%v", err)
	}
	snapshots, err := repository.ListWorkspaceSnapshots(context.Background(), "team-a", "user-a", 10)
	if err != nil || len(snapshots) != 1 || snapshots[0].SourcePath != "project" {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
}
