package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

var (
	ErrSubmissionInvalidOrigin    = errors.New("invalid submission origin")
	ErrSubmissionQueueNotAllowed  = errors.New("submission queue is not allowed")
	ErrSubmissionInvalidJobSpec   = errors.New("invalid job spec")
	ErrSubmissionImageNotAllowed  = errors.New("submission image is not allowed")
	ErrSubmissionGitNotAllowed    = errors.New("submission git source is not allowed")
	ErrSubmissionArtifactNotFound = errors.New("submission artifact not found")
	ErrSubmissionArtifactNotReady = errors.New("submission artifact is not ready")
	ErrSubmissionArtifactInvalid  = errors.New("submission artifact is invalid")
	ErrSubmissionQueueProvision   = errors.New("submission queue provisioning failed")
	ErrSubmissionIdentityPersist  = errors.New("submission identity persistence failed")
	ErrSubmissionIDGeneration     = errors.New("submission id generation failed")
)

type SourceArtifactLookup interface {
	GetSourceArtifact(context.Context, string, string, string) (*domain.SourceArtifact, error)
}

type SubmissionQueueEnsurer func(context.Context, string, string, string) error

type SubmissionServiceOptions struct {
	// Images is the administrator-managed catalogue. When it holds any entry
	// for the tenant it becomes the authoritative allowlist; ImageAllowlist is
	// the fallback for deployments that have not populated it yet.
	Images            ImageStore
	ImageAllowlist    []string
	GitAllowlist      []string
	ClusterQueue      string
	EnsureTenantQueue SubmissionQueueEnsurer
	NewID             func() (string, error)
}

type SubmissionService struct {
	repository        JobRepository
	images            ImageStore
	imageAllowlist    []string
	gitAllowlist      []string
	clusterQueue      string
	ensureTenantQueue SubmissionQueueEnsurer
	newID             func() (string, error)
}

type SubmissionInput struct {
	Principal            auth.Principal
	Spec                 domain.JobSpec
	Origin               domain.SubmissionOrigin
	IdempotencyKey       string
	ExternalSubmissionID string
}

func NewSubmissionService(repository JobRepository, options SubmissionServiceOptions) *SubmissionService {
	newID := options.NewID
	if newID == nil {
		newID = newJobID
	}
	return &SubmissionService{
		repository:        repository,
		images:            options.Images,
		imageAllowlist:    append([]string(nil), options.ImageAllowlist...),
		gitAllowlist:      append([]string(nil), options.GitAllowlist...),
		clusterQueue:      strings.TrimSpace(options.ClusterQueue),
		ensureTenantQueue: options.EnsureTenantQueue,
		newID:             newID,
	}
}

// imagePermitted resolves the requested image against the catalogue first. A
// populated catalogue is the allowlist, so an administrator controls exactly
// which environments can run without also editing deployment values.
func (service *SubmissionService) imagePermitted(ctx context.Context, tenantID, reference string) bool {
	if service.images != nil {
		if _, err := service.images.ImageByReference(ctx, tenantID, domain.ImageKindTraining, reference); err == nil {
			return true
		}
		catalog, err := service.images.ListImages(ctx, tenantID, domain.ImageKindTraining)
		if err == nil && len(catalog) > 0 {
			return false
		}
	}
	return matchesAllowlist(reference, service.imageAllowlist)
}

func (service *SubmissionService) Submit(ctx context.Context, input SubmissionInput) (*domain.TrainingJob, error) {
	if service == nil || service.repository == nil {
		return nil, fmt.Errorf("submission service is not configured")
	}
	if err := input.Origin.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmissionInvalidOrigin, err)
	}
	spec, err := normalizeSubmissionSpec(input.Principal, input.Spec)
	if err != nil {
		return nil, err
	}
	if !service.imagePermitted(ctx, input.Principal.TenantID, spec.Image) {
		return nil, ErrSubmissionImageNotAllowed
	}
	if spec.Source.Type == "git" && !matchesGitAllowlist(spec.Source.URL, service.gitAllowlist) {
		return nil, ErrSubmissionGitNotAllowed
	}
	if spec.Source.Type == "artifact" {
		var materializeErr error
		spec, materializeErr = service.materializeArtifact(ctx, input.Principal, spec)
		if materializeErr != nil {
			return nil, materializeErr
		}
	}
	if err := service.repository.EnsureIdentity(ctx, input.Principal); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmissionIdentityPersist, err)
	}
	if service.ensureTenantQueue != nil {
		namespace := "tenant-" + sanitizeDNS(input.Principal.TenantID)
		if err := service.ensureTenantQueue(ctx, namespace, spec.Queue, service.clusterQueue); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionQueueProvision, err)
		}
	}
	id, err := service.newID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmissionIDGeneration, err)
	}
	job := &domain.TrainingJob{
		ID: id, TenantID: input.Principal.TenantID, UserID: input.Principal.Subject,
		Spec: spec, DesiredState: domain.DesiredActive, ObservedState: domain.StateSubmitted,
		KubernetesNS:     "tenant-" + sanitizeDNS(input.Principal.TenantID),
		SubmissionOrigin: input.Origin, ExternalSubmissionID: strings.TrimSpace(input.ExternalSubmissionID),
	}
	if spec.Source.Type == "artifact" {
		job.SourceArtifactID = spec.Source.ArtifactID
	}
	if err := service.repository.Create(ctx, job, input.IdempotencyKey); err != nil {
		return nil, err
	}
	return job, nil
}

func normalizeSubmissionSpec(principal auth.Principal, spec domain.JobSpec) (domain.JobSpec, error) {
	expectedQueue := tenantQueue(principal.TenantID)
	if spec.Queue == "" {
		spec.Queue = expectedQueue
	} else if spec.Queue != expectedQueue {
		return domain.JobSpec{}, ErrSubmissionQueueNotAllowed
	}
	if err := spec.Validate(); err != nil {
		return domain.JobSpec{}, fmt.Errorf("%w: %v", ErrSubmissionInvalidJobSpec, err)
	}
	return spec, nil
}

func (service *SubmissionService) materializeArtifact(ctx context.Context, principal auth.Principal, spec domain.JobSpec) (domain.JobSpec, error) {
	lookup, ok := service.repository.(SourceArtifactLookup)
	if !ok {
		return domain.JobSpec{}, fmt.Errorf("%w: source artifact repository is not configured", ErrSubmissionArtifactNotFound)
	}
	artifact, err := lookup.GetSourceArtifact(ctx, principal.TenantID, principal.Subject, spec.Source.ArtifactID)
	if err != nil {
		return domain.JobSpec{}, fmt.Errorf("%w: %v", ErrSubmissionArtifactNotFound, err)
	}
	if artifact == nil || artifact.State != domain.SourceArtifactReady {
		return domain.JobSpec{}, ErrSubmissionArtifactNotReady
	}
	if err := artifact.Validate(); err != nil {
		return domain.JobSpec{}, fmt.Errorf("%w: %v", ErrSubmissionArtifactInvalid, err)
	}
	materialized := spec.Source
	materialized.ArtifactObjectKey = artifact.ObjectKey
	materialized.ArtifactSHA256 = artifact.SHA256
	return domain.JobSpec{
		Name: spec.Name, Image: spec.Image, Source: materialized, Entrypoint: spec.Entrypoint, Resources: spec.Resources,
		Queue: spec.Queue, Priority: spec.Priority, DatasetURI: spec.DatasetURI, CheckpointURI: spec.CheckpointURI,
		OutputURI: spec.OutputURI, TimeoutSeconds: spec.TimeoutSeconds, RetryPolicy: spec.RetryPolicy, CleanupPolicy: spec.CleanupPolicy,
	}, nil
}
