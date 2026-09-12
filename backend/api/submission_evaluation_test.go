package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/runtimecatalog"
)

const evaluationRuntimeImage = "harbor.example/evaluator@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func evaluationSubmissionService(repository *submissionServiceRepository, newID func() (string, error)) *SubmissionService {
	dataset, version := streamingDatasetFixtures()
	return NewSubmissionService(repository, SubmissionServiceOptions{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "evaluation-runtime", Name: "Evaluator", Kind: domain.ImageKindTraining,
			Reference: evaluationRuntimeImage, RayVersion: domain.RayVersionCanary,
			SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy:            runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}),
		GitAllowlist:             []string{"git.example"},
		Datasets:                 &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}},
		DatasetVersioningEnabled: true,
		RayDataStreamingEnabled:  true,
		DatasetInternalPrefix:    "ray-train/platform/datasets",
		NewID:                    newID,
	})
}

func evaluationSubmissionSpec() domain.JobSpec {
	spec := streamingSubmissionSpec()
	spec.Name = "eval-job"
	spec.Image = evaluationRuntimeImage
	spec.Source = domain.CodeSource{
		Type:   "git",
		URL:    "https://git.example/platform/evaluator.git",
		Commit: strings.Repeat("1", 40),
	}
	spec.Entrypoint = domain.Entrypoint{Command: []string{"python", "-m", "raytrain_evaluator"}}
	return spec
}

func TestEvaluationSubmissionUsesReservedJobIDAndValidatesFrozenInputs(t *testing.T) {
	repository := &submissionServiceRepository{}
	newIDCalls := 0
	service := evaluationSubmissionService(repository, func() (string, error) {
		newIDCalls++
		return "job-generated-should-not-be-used", nil
	})
	spec := evaluationSubmissionSpec()

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal:                     streamingPrincipal(),
		Spec:                          spec,
		Origin:                        domain.SubmissionOriginEvaluation,
		IdempotencyKey:                "evaluation-request",
		ExternalSubmissionID:          "evaluation-01",
		ReservedJobID:                 "job-0123456789abcdef01234567",
		ExpectedImageDigest:           strings.Repeat("a", 64),
		ExpectedDatasetManifestSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("submit evaluation: %v", err)
	}
	if newIDCalls != 0 {
		t.Fatalf("evaluation reserved ID called generator %d times", newIDCalls)
	}
	if job.ID != "job-0123456789abcdef01234567" || repository.created == nil || repository.created.ID != job.ID {
		t.Fatalf("reserved job ID was not persisted: job=%+v created=%+v", job, repository.created)
	}
	if job.SubmissionOrigin != domain.SubmissionOriginEvaluation || job.ExternalSubmissionID != "evaluation-01" {
		t.Fatalf("evaluation origin metadata not persisted: %+v", job)
	}
	if job.Spec.Source.Commit != strings.Repeat("1", 40) {
		t.Fatalf("evaluator git commit was not preserved as frozen source: %+v", job.Spec.Source)
	}
	if job.Spec.Image != evaluationRuntimeImage || job.DatasetProvenance.ManifestSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("frozen runtime or dataset mismatch: image=%q provenance=%+v", job.Spec.Image, job.DatasetProvenance)
	}
}

func TestEvaluationSubmissionRejectsReservedControlsForNormalOrigins(t *testing.T) {
	service := evaluationSubmissionService(&submissionServiceRepository{}, func() (string, error) { return "job-normal", nil })
	spec := evaluationSubmissionSpec()
	for _, input := range []SubmissionInput{
		{ReservedJobID: "job-0123456789abcdef01234567"},
		{ExpectedImageDigest: strings.Repeat("a", 64)},
		{ExpectedDatasetManifestSHA256: strings.Repeat("b", 64)},
	} {
		input.Principal = streamingPrincipal()
		input.Spec = spec
		input.Origin = domain.SubmissionOriginPortal
		_, err := service.Submit(context.Background(), input)
		if !errors.Is(err, ErrSubmissionInvalidOrigin) {
			t.Fatalf("normal origin accepted evaluation-only controls %+v: %v", input, err)
		}
	}
}

func TestEvaluationSubmissionRejectsMismatchedFrozenRuntimeOrDataset(t *testing.T) {
	tests := []struct {
		name   string
		input  SubmissionInput
		detail string
	}{
		{
			name: "bad reserved id",
			input: SubmissionInput{
				ReservedJobID:                 "manual-id",
				ExpectedImageDigest:           strings.Repeat("a", 64),
				ExpectedDatasetManifestSHA256: strings.Repeat("b", 64),
			},
			detail: "reserved job",
		},
		{
			name: "image digest mismatch",
			input: SubmissionInput{
				ReservedJobID:                 "job-0123456789abcdef01234567",
				ExpectedImageDigest:           strings.Repeat("c", 64),
				ExpectedDatasetManifestSHA256: strings.Repeat("b", 64),
			},
			detail: "image digest",
		},
		{
			name: "dataset manifest mismatch",
			input: SubmissionInput{
				ReservedJobID:                 "job-0123456789abcdef01234567",
				ExpectedImageDigest:           strings.Repeat("a", 64),
				ExpectedDatasetManifestSHA256: strings.Repeat("c", 64),
			},
			detail: "dataset manifest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := evaluationSubmissionService(repository, func() (string, error) { return "job-generated", nil })
			input := test.input
			input.Principal = streamingPrincipal()
			input.Spec = evaluationSubmissionSpec()
			input.Origin = domain.SubmissionOriginEvaluation
			_, err := service.Submit(context.Background(), input)
			if !errors.Is(err, ErrSubmissionInvalidOrigin) || !strings.Contains(err.Error(), test.detail) {
				t.Fatalf("expected %s rejection, got %v", test.detail, err)
			}
			if repository.created != nil || repository.createCalls != 0 {
				t.Fatalf("invalid evaluation crossed persistence boundary: %+v", repository.created)
			}
		})
	}
}

func TestEvaluationSubmissionRequiresPinnedGitCommit(t *testing.T) {
	service := evaluationSubmissionService(&submissionServiceRepository{}, func() (string, error) { return "job-generated", nil })
	spec := evaluationSubmissionSpec()
	spec.Source.Commit = "0123456789abcdef"
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal:                     streamingPrincipal(),
		Spec:                          spec,
		Origin:                        domain.SubmissionOriginEvaluation,
		ReservedJobID:                 "job-0123456789abcdef01234567",
		ExpectedImageDigest:           strings.Repeat("a", 64),
		ExpectedDatasetManifestSHA256: strings.Repeat("b", 64),
	})
	if !errors.Is(err, ErrSubmissionInvalidOrigin) || !strings.Contains(err.Error(), "git commit") {
		t.Fatalf("expected pinned evaluator git commit rejection, got %v", err)
	}
}

func TestSubmissionOriginEvaluationValidation(t *testing.T) {
	if err := domain.SubmissionOriginEvaluation.Validate(); err != nil {
		t.Fatalf("evaluation origin must be valid: %v", err)
	}
}
