package repositories

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func TestIDCDataSyncRepositoryCreatesOneActiveRunAndFinalizesInventory(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&IDCDataSyncConnectorRecord{}, &IDCDataSyncRunRecord{}, &IDCDataSyncInventoryEntryRecord{}, &IDCDataSyncObjectRefRecord{}); err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	connector := domain.IDCDataSyncConnector{ID: "idc-sync-labeled", Name: "labeled", SourceSpace: domain.DataSpaceIDCOriginal, SourceRelativePath: "QP_NuScene/labeled", MirrorPrefix: "ray-train/platform/idc-mirror/labeled", Enabled: true, CreatedBy: "admin-1"}
	if err := repository.CreateIDCDataSyncConnector(context.Background(), connector); err != nil {
		t.Fatal(err)
	}
	run := domain.IDCDataSyncRun{ID: "sync-run-1", ConnectorID: connector.ID, IdempotencyKey: "request-1", Mode: domain.IDCDataSyncRunModeSync, State: domain.IDCDataSyncRunPending, RequestedBy: "admin-1"}
	if err := repository.CreateIDCDataSyncRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateIDCDataSyncRun(context.Background(), domain.IDCDataSyncRun{ID: "sync-run-2", ConnectorID: connector.ID, IdempotencyKey: "request-2", Mode: domain.IDCDataSyncRunModeSync, State: domain.IDCDataSyncRunPending, RequestedBy: "admin-1"}); err != ErrIDCDataSyncActiveRun {
		t.Fatalf("concurrent run error = %v, want %v", err, ErrIDCDataSyncActiveRun)
	}
	if _, claimed, err := repository.ClaimIDCDataSyncRun(context.Background(), run.ID, time.Now().UTC()); err != nil || !claimed {
		t.Fatalf("ClaimIDCDataSyncRun() = claimed=%v err=%v", claimed, err)
	}
	entry := domain.IDCDataSyncInventoryEntry{RunID: run.ID, RelativePath: "site-a/frame.bin", SizeBytes: 42, ModifiedAt: time.Now().UTC(), SHA256: strings.Repeat("a", 64), ObjectKey: "ray-train/platform/idc-raw/sha256/aa/" + strings.Repeat("a", 64)}
	if err := repository.AppendIDCDataSyncInventory(context.Background(), run.ID, []domain.IDCDataSyncInventoryEntry{entry}); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.CompleteIDCDataSyncRun(context.Background(), run.ID, domain.IDCDataSyncCompletion{
		InventorySHA256: strings.Repeat("b", 64), InventoryObjectKey: "ray-train/platform/idc-inventories/sync-run-1/" + strings.Repeat("b", 64) + ".json",
		NewObjectCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.IDCDataSyncRunSucceeded || completed.InventorySHA256 != strings.Repeat("b", 64) {
		t.Fatalf("completed run = %+v", completed)
	}
	entries, err := repository.ListIDCDataSyncInventory(context.Background(), run.ID)
	if err != nil || len(entries) != 1 || entries[0].ObjectKey != entry.ObjectKey {
		t.Fatalf("inventory = %+v, %v", entries, err)
	}
	runs, err := repository.ListIDCDataSyncRuns(context.Background(), connector.ID)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].InventoryObjectKey != "ray-train/platform/idc-inventories/sync-run-1/"+strings.Repeat("b", 64)+".json" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
}

func TestIDCDataSyncRepositoryReturnsPreviousInventoryAndConvergesFailure(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&IDCDataSyncConnectorRecord{}, &IDCDataSyncRunRecord{}, &IDCDataSyncInventoryEntryRecord{}, &IDCDataSyncObjectRefRecord{}); err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	connector := domain.IDCDataSyncConnector{ID: "idc-sync-labeled", Name: "labeled", SourceSpace: domain.DataSpaceIDCOriginal, SourceRelativePath: "QP_NuScene/labeled", MirrorPrefix: "ray-train/platform/idc-mirror/labeled", Enabled: true, CreatedBy: "admin-1"}
	if err := repository.CreateIDCDataSyncConnector(context.Background(), connector); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	finished := now.Add(-time.Hour)
	previous := IDCDataSyncRunRecord{ID: "previous", ConnectorID: connector.ID, IdempotencyKey: "previous", Mode: string(domain.IDCDataSyncRunModeSync), State: string(domain.IDCDataSyncRunSucceeded), RequestedBy: "admin-1", InventorySHA256: strings.Repeat("b", 64), InventoryObjectKey: "ray-train/platform/idc-inventories/previous/" + strings.Repeat("b", 64) + ".json", CreatedAt: finished, StartedAt: &finished, FinishedAt: &finished}
	if err := database.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	latest, found, err := repository.LatestSuccessfulIDCDataSyncRun(context.Background(), connector.ID)
	if err != nil || !found || latest.ID != "previous" {
		t.Fatalf("latest=%+v found=%v err=%v", latest, found, err)
	}
	byID, foundByID, err := repository.GetIDCDataSyncRun(context.Background(), previous.ID)
	if err != nil || !foundByID || byID.InventorySHA256 != previous.InventorySHA256 || byID.InventoryObjectKey != previous.InventoryObjectKey {
		t.Fatalf("byID=%+v found=%t err=%v", byID, foundByID, err)
	}
	run := domain.IDCDataSyncRun{ID: "current", ConnectorID: connector.ID, IdempotencyKey: "current", Mode: domain.IDCDataSyncRunModeSync, State: domain.IDCDataSyncRunPending, RequestedBy: "admin-1"}
	if err := repository.CreateIDCDataSyncRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := repository.ClaimIDCDataSyncRun(context.Background(), run.ID, now); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	failed, err := repository.FailIDCDataSyncRun(context.Background(), run.ID, "Kubernetes Job failed", now.Add(time.Minute))
	if err != nil || failed.State != domain.IDCDataSyncRunFailed || failed.FailureReason != "Kubernetes Job failed" || failed.FinishedAt == nil {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func TestIDCDataSyncRepositoryUpdatesOnlyConnectorPolicy(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&IDCDataSyncConnectorRecord{}); err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	connector := domain.IDCDataSyncConnector{ID: "labeled", Name: "Labeled", SourceSpace: domain.DataSpaceIDCOriginal, SourceRelativePath: "QP_NuScene/labeled", MirrorPrefix: "ray-train/platform/idc-mirror/labeled", Enabled: true, CreatedBy: "admin"}
	if err := repository.CreateIDCDataSyncConnector(context.Background(), connector); err != nil {
		t.Fatal(err)
	}
	connector.Enabled = false
	connector.SyncIntervalMinutes = 60
	updated, err := repository.UpdateIDCDataSyncConnector(context.Background(), connector)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.SyncIntervalMinutes != 60 || updated.SourceRelativePath != "QP_NuScene/labeled" {
		t.Fatalf("unexpected connector: %#v", updated)
	}
}
