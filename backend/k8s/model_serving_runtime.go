package k8s

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"ray-train-platform-backend/domain"
)

// Restore only the trusted runtime after managed-attempt CAS returns a JSON
// snapshot. The current attempt lease/fence/UID must remain untouched.
func (r *Reconciler) restoreServingRuntime(ctx context.Context, job *domain.TrainingJob) (*domain.TrainingJob, error) {
	if job.SubmissionOrigin != domain.SubmissionOriginServing || job.Spec.ServingRuntime != nil {
		return job, nil
	}
	loaded, err := r.store.GetByID(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if loaded == nil || loaded.ID != job.ID || loaded.TenantID != job.TenantID || loaded.UserID != job.UserID || loaded.SubmissionOrigin != domain.SubmissionOriginServing || loaded.Spec.ServingRuntime == nil {
		return nil, fmt.Errorf("trusted serving runtime does not match the current job")
	}
	if err := loaded.Spec.ServingRuntime.ValidateCodeSource(job.Spec.Source); err != nil {
		return nil, err
	}
	restored := *job
	runtime := *loaded.Spec.ServingRuntime
	restored.Spec.ServingRuntime = &runtime
	return &restored, nil
}

func appendServingRuntime(environment []any, runtime *domain.ServingRuntime, options RenderOptions) []any {
	if runtime == nil {
		return environment
	}
	values := [][2]string{
		{"MODEL_SERVING_ID", runtime.DeploymentID},
		{"MODEL_SERVING_MODEL_SHA256", runtime.ModelSHA256},
		{"MODEL_SERVING_MODEL_SIZE_BYTES", strconv.FormatInt(runtime.ModelSizeBytes, 10)},
		{"MODEL_SERVING_PROTOCOL", runtime.Protocol},
		{"MODEL_SERVING_BASE_URL", strings.TrimRight(options.TrainingEventBaseURL, "/") + "/jobs/" + options.trainingEventJobID + "/model-serving"},
		{"MODEL_SERVING_PORT", "8000"},
	}
	for _, value := range values {
		environment = setEnvironmentValue(environment, value[0], value[1])
	}
	return environment
}

func servingCodeMaterializerCommand(spec domain.JobSpec, options RenderOptions) string {
	runtime := spec.ServingRuntime
	if runtime == nil || runtime.ValidateCodeSource(spec.Source) != nil || validateEvaluationCodeBaseURL(options.TrainingEventBaseURL) != nil {
		return "echo 'source materialization failed: invalid serving code snapshot' >&2\nexit 1\n"
	}
	archive := "/tmp/platform-serving-code.zip"
	command := []string{"python3", "/usr/local/bin/platform-fetch-serving-code.py", "--base-url", options.TrainingEventBaseURL, "--job-id", options.trainingEventJobID,
		"--token-file", trainingEventTokenMountPath + "/" + TrainingEventTokenKey, "--sha256", runtime.CodeSHA256, "--size-bytes", strconv.FormatInt(runtime.CodeSizeBytes, 10), "--output", archive}
	extract := []string{"python3", "/usr/local/bin/platform-safe-extract.py", "--archive", archive, "--destination", "/workspace", "--max-uncompressed-bytes", "268435456"}
	entry := append(append([]string(nil), spec.Entrypoint.Command...), spec.Entrypoint.Args...)
	if len(entry) >= 3 && entry[1] == "-m" {
		extract = append(extract, "--required-module", entry[2])
	} else if len(entry) >= 2 {
		extract = append(extract, "--required-script", entry[1])
	}
	return shellJoin(command) + "\n" + shellJoin(extract) + "\nrm -f " + shellQuote(archive) + "\n"
}
