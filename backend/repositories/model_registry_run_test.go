package repositories

import (
	"context"
	"errors"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelregistry"
	"strings"
	"testing"
)

func TestRegistryDashboardRunRequiresOneReadySharedSnapshot(t *testing.T) {
	base := modelTestStore(t)
	if err := base.db.AutoMigrate(&mr.Record{}); err != nil {
		t.Fatal(err)
	}
	model := ml.Model{ID: "registry-model", Name: "shared", OwnerID: "owner", TenantID: "other-team", Archived: true}
	version := ml.Version{ID: "registry-version", ModelID: model.ID, Number: 1, State: ml.Ready}
	run := strings.Repeat("a", 32)
	record := mr.Record{VersionID: version.ID, State: "READY", RunID: run, SourceURI: "mlflow-artifacts:/11/" + run + "/artifacts/checkpoint/" + strings.Repeat("b", 64) + ".pt"}
	for _, item := range []any{&model, &version, &record} {
		if err := base.db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := NewModelRegistryStore(base.db)
	ctx := context.Background()
	got, err := store.GetReadyByRunID(ctx, run)
	if err != nil || got.VersionID != version.ID {
		t.Fatalf("shared archived model lost run link: %+v %v", got, err)
	}
	missing, err := store.GetReadyByRunID(ctx, strings.Repeat("c", 32))
	if err != nil || missing.State != "NOT_LINKED" {
		t.Fatalf("unlinked run accepted: %+v %v", missing, err)
	}
	if err := base.db.Model(&record).Update("state", "SYNCING").Error; err != nil {
		t.Fatal(err)
	}
	missing, err = store.GetReadyByRunID(ctx, run)
	if err != nil || missing.State != "NOT_LINKED" {
		t.Fatalf("unfinished registry accepted: %+v %v", missing, err)
	}
	if err := base.db.Model(&record).Update("state", "READY").Error; err != nil {
		t.Fatal(err)
	}
	if err := base.db.Model(&version).Update("state", "FAILED").Error; err != nil {
		t.Fatal(err)
	}
	missing, err = store.GetReadyByRunID(ctx, run)
	if err != nil || missing.State != "NOT_LINKED" {
		t.Fatalf("nonready snapshot accepted: %+v %v", missing, err)
	}
	if err := base.db.Model(&version).Update("state", ml.Ready).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := ml.Version{ID: "second-version", ModelID: model.ID, Number: 2, State: ml.Ready}
	if err := base.db.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	second := record
	second.VersionID = duplicate.ID
	if err := base.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetReadyByRunID(ctx, run); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("ambiguous linked run accepted: %v", err)
	}
}
