package main

import (
	"context"
	"encoding/json"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/modelevaluation"
	"strings"
	"testing"
)

func TestTrustedEvaluationArchiveCodeLoadedOnlyFromFrozenEvaluator(t *testing.T) {
	source := domain.CodeSource{Type: "evaluation-archive", ArtifactID: strings.Repeat("c", 32), ArtifactSHA256: strings.Repeat("d", 64)}
	job := &domain.TrainingJob{ID: "job-1", TenantID: "team-1", UserID: "owner-1", SubmissionOrigin: domain.SubmissionOriginEvaluation, Spec: domain.JobSpec{Source: source}}
	record := modelevaluation.Evaluation{ID: "evaluation-1", JobID: job.ID, TenantID: job.TenantID, OwnerID: job.UserID, ModelSHA256: strings.Repeat("a", 64), Dataset: modelevaluation.DatasetSnapshot{ManifestSHA256: strings.Repeat("b", 64), Split: "test", SampleCount: 10}, Evaluator: modelevaluation.Evaluator{ID: "evaluator-1", Protocol: "model-evaluation-report/v1", Code: &modelevaluation.CodeSnapshot{ID: source.ArtifactID, SHA256: source.ArtifactSHA256, SizeBytes: 1234, Format: "zip"}}, Config: json.RawMessage(`{}`), ConfigSHA256: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}
	loaded, err := loadTrustedEvaluationRuntime(context.Background(), job, &evaluationLookupStub{value: record})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Spec.EvaluationRuntime.CodeID != source.ArtifactID || loaded.Spec.EvaluationRuntime.CodeSizeBytes != 1234 {
		t.Fatal("code provenance was not loaded")
	}
	for _, mutate := range []func(*domain.TrainingJob){func(v *domain.TrainingJob) { v.Spec.Source.ArtifactID = strings.Repeat("e", 32) }, func(v *domain.TrainingJob) { v.Spec.Source.ArtifactSHA256 = strings.Repeat("e", 64) }, func(v *domain.TrainingJob) { v.Spec.Source.Type = "git" }, func(v *domain.TrainingJob) { v.Spec.Source.URI = "tos://private/code" }} {
		candidate := *job
		mutate(&candidate)
		if _, err := loadTrustedEvaluationRuntime(context.Background(), &candidate, &evaluationLookupStub{value: record}); err == nil {
			t.Fatal("job source differs from frozen code")
		}
	}
	record.Evaluator.Code = nil
	if _, err := loadTrustedEvaluationRuntime(context.Background(), job, &evaluationLookupStub{value: record}); err == nil {
		t.Fatal("archive job loaded without frozen code")
	}
}
