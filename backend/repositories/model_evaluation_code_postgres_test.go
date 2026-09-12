package repositories

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

func TestEvaluationCodePostgresSnapshotRoundTripAndImmutability(t *testing.T) {
	store, reader := evaluationPostgresStores(t, false)
	ctx := context.Background()
	base := seedEvaluationSources(t, store)
	uploaded := base.Evaluator
	uploaded.ID = strings.Repeat("7", 32)
	uploaded.Name = "uploaded evaluator"
	uploaded.GitURL, uploaded.GitCommit = "", ""
	uploaded.Code = &me.CodeSnapshot{ID: uploaded.ID, SHA256: strings.Repeat("8", 64), SizeBytes: 12345, Format: "zip"}
	saved, err := store.CreateEvaluator(ctx, uploaded)
	if err != nil { t.Fatal(err) }
	base.Evaluator = saved
	base.JobSpec.Source = domain.CodeSource{Type: "evaluation-archive", ArtifactID: saved.Code.ID, ArtifactSHA256: saved.Code.SHA256}
	frozen, created, err := store.ReserveEvaluation(ctx, base)
	if err != nil || !created { t.Fatalf("archive reservation: created=%v err=%v", created, err) }
	loaded, err := reader.GetEvaluation(ctx, frozen.ID, frozen.TenantID, false)
	if err != nil { t.Fatal(err) }
	if loaded.Evaluator.Code == nil || *loaded.Evaluator.Code != *saved.Code || loaded.JobSpec.Source != base.JobSpec.Source {
		t.Fatalf("archive identity changed after JSONB read: %+v", loaded.Evaluator.Code)
	}
	if me.ComparisonFingerprint(loaded) != me.ComparisonFingerprint(frozen) {
		t.Fatal("JSONB round trip changed evaluation comparison fingerprint")
	}
	if _, err := store.SetEvaluatorActive(ctx, saved.ID, false, saved.Revision); err != nil { t.Fatal(err) }
	historical, err := reader.GetEvaluation(ctx, frozen.ID, frozen.TenantID, false)
	if err != nil || historical.Evaluator.Code == nil || *historical.Evaluator.Code != *saved.Code || !historical.Evaluator.Active {
		t.Fatalf("disabling plan changed historical code snapshot: %v", err)
	}
	changed := saved
	changedCode := *saved.Code
	changedCode.SHA256 = strings.Repeat("9", 64)
	changed.Code = &changedCode
	raw, err := json.Marshal(changed)
	if err != nil { t.Fatal(err) }
	if err := store.db.Model(&modelEvaluatorRecord{}).Where("id = ?", saved.ID).Update("snapshot_json", string(raw)).Error; err == nil {
		t.Fatal("database allowed changing a registered code snapshot")
	}
}
