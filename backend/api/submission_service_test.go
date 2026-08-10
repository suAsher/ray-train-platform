package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type submissionServiceRepository struct {
	created        *domain.TrainingJob
	identityCalls  int
	artifact       *domain.SourceArtifact
	artifactLookup string
}

func (repository *submissionServiceRepository) Create(_ context.Context, job *domain.TrainingJob, _ string) error {
	copy := *job
	repository.created = &copy
	return nil
}

func (repository *submissionServiceRepository) Get(_ context.Context, _, _ string) (*domain.TrainingJob, error) {
	return nil, context.Canceled
}

func (repository *submissionServiceRepository) List(_ context.Context, _ domain.JobFilter) (domain.Page[domain.TrainingJob], error) {
	return domain.Page[domain.TrainingJob]{}, nil
}

func (repository *submissionServiceRepository) SetDesiredState(_ context.Context, _, _ string, _ domain.DesiredState) error {
	return nil
}

func (repository *submissionServiceRepository) EnsureIdentity(_ context.Context, _ auth.Principal) error {
	repository.identityCalls++
	return nil
}

func (repository *submissionServiceRepository) GetSourceArtifact(_ context.Context, tenantID, userID, artifactID string) (*domain.SourceArtifact, error) {
	repository.artifactLookup = tenantID + "/" + userID + "/" + artifactID
	if repository.artifact == nil || repository.artifact.TenantID != tenantID || repository.artifact.UserID != userID || repository.artifact.ID != artifactID {
		return nil, ErrSubmissionArtifactNotFound
	}
	copy := *repository.artifact
	return &copy, nil
}

func readySubmissionArtifact(t *testing.T) domain.SourceArtifact {
	t.Helper()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: "artifact-01", TenantID: "tenant-a", UserID: "user-01", SHA256: strings.Repeat("a", 64), SizeBytes: 100,
	}, now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("new source artifact: %v", err)
	}
	ready, err := artifact.MarkReady(now)
	if err != nil {
		t.Fatalf("mark source artifact ready: %v", err)
	}
	return ready
}

func artifactSubmissionSpec() domain.JobSpec {
	return domain.JobSpec{
		Name:       "artifact-job",
		Image:      "registry.example/ray@sha256:" + strings.Repeat("b", 64),
		Source:     domain.CodeSource{Type: "artifact", ArtifactID: "artifact-01"},
		Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1},
	}
}

func TestSubmissionServiceMaterializesReadyOwnerScopedArtifactForRayCLI(t *testing.T) {
	artifact := readySubmissionArtifact(t)
	repository := &submissionServiceRepository{artifact: &artifact}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		NewID: func() (string, error) { return "job-ray-cli", nil },
	})
	principal := auth.Principal{Subject: "user-01", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: principal, Spec: artifactSubmissionSpec(), Origin: domain.SubmissionOriginRayCLI,
	})
	if err != nil {
		t.Fatalf("submit artifact job: %v", err)
	}
	if repository.artifactLookup != "tenant-a/user-01/artifact-01" {
		t.Fatalf("artifact lookup is not owner scoped: %q", repository.artifactLookup)
	}
	if repository.created == nil || repository.identityCalls != 1 {
		t.Fatalf("submission did not persist identity and job: identity=%d job=%+v", repository.identityCalls, repository.created)
	}
	if job.SourceArtifactID != artifact.ID || job.SubmissionOrigin != domain.SubmissionOriginRayCLI || job.Spec.Source.ArtifactObjectKey != artifact.ObjectKey || job.Spec.Source.ArtifactSHA256 != artifact.SHA256 {
		t.Fatalf("artifact was not materialized into immutable job data: %+v", job)
	}
}

func TestSubmissionServiceRejectsNonReadyArtifactBeforePersistence(t *testing.T) {
	artifact := readySubmissionArtifact(t)
	artifact.State = domain.SourceArtifactPending
	repository := &submissionServiceRepository{artifact: &artifact}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		NewID: func() (string, error) { return "job-not-ready", nil },
	})
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-01", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC},
		Spec:      artifactSubmissionSpec(), Origin: domain.SubmissionOriginAPI,
	})
	if err == nil {
		t.Fatal("pending artifact was accepted")
	}
	if repository.created != nil {
		t.Fatalf("pending artifact reached persistence: %+v", repository.created)
	}
}
