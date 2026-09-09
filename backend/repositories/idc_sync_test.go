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
	completed, err := repository.CompleteIDCDataSyncRun(context.Background(), run.ID, strings.Repeat("b", 64), "ray-train/platform/idc-inventories/sync-run-1/"+strings.Repeat("b", 64)+".json", []domain.IDCDataSyncInventoryEntry{entry})
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
}
