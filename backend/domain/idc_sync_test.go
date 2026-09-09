package domain

import (
	"strings"
	"testing"
	"time"
)

func TestIDCDataSyncConnectorRequiresGovernedReadOnlyOriginalSource(t *testing.T) {
	valid := IDCDataSyncConnector{
		ID: "idc-sync-labeled", Name: "QP NuScene labeled", SourceSpace: DataSpaceIDCOriginal,
		SourceRelativePath: "QP_NuScene/labeled", MirrorPrefix: "ray-train/platform/idc-mirror/qpnuscenes-labeled",
		Enabled: true, CreatedBy: "admin-1",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, mutate := range map[string]func(*IDCDataSyncConnector){
		"arbitrary data space": func(item *IDCDataSyncConnector) { item.SourceSpace = DataSpaceMyFiles },
		"absolute source":      func(item *IDCDataSyncConnector) { item.SourceRelativePath = "/etc" },
		"root mirror":          func(item *IDCDataSyncConnector) { item.MirrorPrefix = "." },
		"unknown creator":      func(item *IDCDataSyncConnector) { item.CreatedBy = "" },
	} {
		t.Run(name, func(t *testing.T) {
			item := valid
			mutate(&item)
			if err := item.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestIDCDataSyncRunRequiresImmutableInventoryOnSuccess(t *testing.T) {
	now := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	valid := IDCDataSyncRun{
		ID: "idc-sync-run-1", ConnectorID: "idc-sync-labeled", Mode: IDCDataSyncRunModeSync,
		State: IDCDataSyncRunSucceeded, RequestedBy: "admin-1", IdempotencyKey: "request-1", StartedAt: &now, FinishedAt: &now,
		InventorySHA256: strings.Repeat("a", 64), InventoryObjectKey: "ray-train/platform/idc-inventories/idc-sync-run-1/" + strings.Repeat("a", 64) + ".json", SourceObjectCount: 2, SourceBytes: 42,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, mutate := range map[string]func(*IDCDataSyncRun){
		"successful run has no inventory":        func(item *IDCDataSyncRun) { item.InventorySHA256 = "" },
		"successful run has no inventory object": func(item *IDCDataSyncRun) { item.InventoryObjectKey = "" },
		"bad digest":                             func(item *IDCDataSyncRun) { item.InventorySHA256 = "not-a-digest" },
		"negative bytes":                         func(item *IDCDataSyncRun) { item.SourceBytes = -1 },
		"running run has finished time":          func(item *IDCDataSyncRun) { item.State = IDCDataSyncRunRunning },
	} {
		t.Run(name, func(t *testing.T) {
			item := valid
			mutate(&item)
			if err := item.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestIDCDataSyncInventoryEntryBindsObjectDigest(t *testing.T) {
	entry := IDCDataSyncInventoryEntry{
		RunID: "idc-sync-run-1", RelativePath: "site-a/frame.bin", SizeBytes: 42,
		ModifiedAt: time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC), SHA256: strings.Repeat("a", 64),
		ObjectKey: "ray-train/platform/idc-raw/sha256/aa/" + strings.Repeat("a", 64),
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	entry.ObjectKey = "ray-train/platform/idc-mirror/site-a/frame.bin"
	if err := entry.Validate(); err == nil {
		t.Fatal("mutable mirror key was accepted as immutable object")
	}
}
