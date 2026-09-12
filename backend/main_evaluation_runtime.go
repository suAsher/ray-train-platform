package main

import (
	"context"
	"fmt"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/modelevaluation"
)

type evaluationRuntimeLookup interface {
	GetEvaluationByJobID(context.Context, string) (modelevaluation.Evaluation, error)
}

func loadTrustedEvaluationRuntime(ctx context.Context, job *domain.TrainingJob, lookup evaluationRuntimeLookup) (*domain.TrainingJob, error) {
	if job == nil {
		return nil, fmt.Errorf("job was not found")
	}
	loaded := *job
	loaded.Spec.EvaluationRuntime = nil
	if job.SubmissionOrigin != domain.SubmissionOriginEvaluation {
		return &loaded, nil
	}
	if lookup == nil {
		return nil, fmt.Errorf("evaluation runtime repository is not configured")
	}
	evaluation, err := lookup.GetEvaluationByJobID(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("load trusted evaluation context: %w", err)
	}
	if evaluation.JobID != job.ID || evaluation.TenantID != job.TenantID || evaluation.OwnerID != job.UserID {
		return nil, fmt.Errorf("evaluation ownership does not match job")
	}
	runtime := &domain.EvaluationRuntime{
		EvaluationID: evaluation.ID, ModelSHA256: evaluation.ModelSHA256,
		DatasetManifestSHA256: evaluation.Dataset.ManifestSHA256, DatasetSplit: evaluation.Dataset.Split, DatasetSampleCount: evaluation.Dataset.SampleCount,
		ConfigJSON: string(evaluation.Config), ConfigSHA256: evaluation.ConfigSHA256,
		EvaluatorID: evaluation.Evaluator.ID, Protocol: evaluation.Evaluator.Protocol,
	}
	if code := evaluation.Evaluator.Code; code != nil {
		runtime.CodeID, runtime.CodeSHA256, runtime.CodeSizeBytes, runtime.CodeFormat = code.ID, code.SHA256, code.SizeBytes, code.Format
	}
	if err := runtime.Validate(); err != nil {
		return nil, err
	}
	if err := runtime.ValidateCodeSource(job.Spec.Source); err != nil {
		return nil, err
	}
	loaded.Spec.EvaluationRuntime = runtime
	return &loaded, nil
}
