package repositories

import (
	"context"
	"errors"
	"testing"

	ss "ray-train-platform-backend/storagesync"
)

func TestStorageSyncPostgresGCReadsDurablePreviousAttempt(t *testing.T) {
	writer, reader := storageSyncPostgresStores(t, false)
	ctx := context.Background()
	old := storageSyncSeed(t, writer, "gc-history")
	old.State = "PAUSED"
	old.Phase = "TRANSFER"
	old.JobUID = "stopped-job"
	old.ReceiptState = "PAUSED"
	old.StopVerified = true
	old.RequestsDrained = true
	if err := writer.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(old) }); err != nil {
		t.Fatal(err)
	}
	current := old
	current.Attempt++
	current.Generation++
	current.State = "QUEUED"
	current.JobUID = ""
	current.ReceiptState = ""
	current.StopVerified = false
	current.RequestsDrained = false
	if err := writer.Transact(ctx, func(tx ss.Tx) error { return tx.PutRun(current) }); err != nil {
		t.Fatal(err)
	}
	got, err := reader.GetAttempt(ctx, old.ID, old.Attempt)
	if err != nil || got.JobUID != old.JobUID || !got.StopVerified || !got.RequestsDrained || got.ReceiptState != "PAUSED" || got.Attempt != 1 {
		t.Fatalf("lost previous stop proof: %+v %v", got, err)
	}
	if _, err = reader.GetAttempt(ctx, old.ID, 99); !errors.Is(err, ss.ErrNotFound) {
		t.Fatalf("unknown attempt: %v", err)
	}
	p := ss.Preview{ID: "gc-preview", PlanID: old.PlanID, Actor: "admin", Kind: "PREVIEW", State: "SUCCEEDED", Attempt: 1, Generation: 1, StopVerified: true, JobUID: "preview-uid"}
	if err = writer.Transact(ctx, func(tx ss.Tx) error { return tx.PutPreview(p) }); err != nil {
		t.Fatal(err)
	}
	persisted, err := reader.GetPreview(ctx, p.ID)
	if err != nil || !persisted.StopVerified {
		t.Fatalf("preview proof did not survive reload: %+v %v", persisted, err)
	}
}
