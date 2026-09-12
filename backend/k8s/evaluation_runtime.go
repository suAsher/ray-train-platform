package k8s

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"ray-train-platform-backend/domain"
)

type EvaluationFinalizer interface {
	FinalizeEvaluationJob(context.Context, *domain.TrainingJob) error
}

func (r *Reconciler) WithEvaluationFinalizer(finalizer EvaluationFinalizer) *Reconciler {
	r.evaluationJobs = finalizer
	return r
}

// Managed-attempt CAS methods return fresh DB snapshots without JSON-excluded
// fields. Recover only the trusted immutable evaluation context; never replace
// the newly acquired attempt state or creation lease with the reloaded snapshot.
func (r *Reconciler) restoreEvaluationRuntime(ctx context.Context, job *domain.TrainingJob) (*domain.TrainingJob, error) {
	if job.SubmissionOrigin != domain.SubmissionOriginEvaluation || job.Spec.EvaluationRuntime != nil {
		return job, nil
	}
	loaded, err := r.store.GetByID(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if loaded == nil || loaded.ID != job.ID || loaded.TenantID != job.TenantID || loaded.UserID != job.UserID || loaded.SubmissionOrigin != domain.SubmissionOriginEvaluation || loaded.Spec.EvaluationRuntime == nil {
		return nil, fmt.Errorf("trusted evaluation runtime does not match the current job")
	}
	restored := *job
	runtime := *loaded.Spec.EvaluationRuntime
	restored.Spec.EvaluationRuntime = &runtime
	return &restored, nil
}

func appendEvaluationRuntime(environment []any, runtime *domain.EvaluationRuntime, options RenderOptions) []any {
	if runtime == nil {
		return environment
	}
	values := [][2]string{
		{"MODEL_EVALUATION_ID", runtime.EvaluationID},
		{"MODEL_EVALUATION_MODEL_SHA256", runtime.ModelSHA256},
		{"MODEL_EVALUATION_DATASET_MANIFEST_SHA256", runtime.DatasetManifestSHA256},
		{"MODEL_EVALUATION_DATASET_SPLIT", runtime.DatasetSplit},
		{"MODEL_EVALUATION_DATASET_SAMPLE_COUNT", strconv.FormatInt(runtime.DatasetSampleCount, 10)},
		{"MODEL_EVALUATION_CONFIG_JSON", runtime.ConfigJSON},
		{"MODEL_EVALUATION_CONFIG_SHA256", runtime.ConfigSHA256},
		{"MODEL_EVALUATION_EVALUATOR_ID", runtime.EvaluatorID},
		{"MODEL_EVALUATION_PROTOCOL", runtime.Protocol},
		{"MODEL_EVALUATION_BASE_URL", strings.TrimRight(options.TrainingEventBaseURL, "/") + "/jobs/" + options.trainingEventJobID + "/model-evaluation"},
	}
	for _, value := range values {
		environment = setEnvironmentValue(environment, value[0], value[1])
	}
	return environment
}
