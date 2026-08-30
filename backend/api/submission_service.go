package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
	"ray-train-platform-backend/runtimecatalog"
)

var (
	ErrSubmissionInvalidOrigin   = errors.New("invalid submission origin")
	ErrSubmissionQueueNotAllowed = errors.New("submission queue is not allowed")
	ErrSubmissionInvalidJobSpec  = errors.New("invalid job spec")
	ErrSubmissionImageNotAllowed = errors.New("submission image is not allowed")
	ErrSubmissionGitNotAllowed   = errors.New("submission git source is not allowed")
	// ErrSubmissionCodeSourceNotAllowed keeps the public training contract
	// intentionally small. Code enters a Ray Pod only through a Git checkout
	// or an owner-scoped workspace snapshot; neither requires a TOS credential
	// in the job's init container.
	ErrSubmissionCodeSourceNotAllowed      = errors.New("submission code source is not allowed")
	ErrSubmissionArtifactNotFound          = errors.New("submission artifact not found")
	ErrSubmissionArtifactNotReady          = errors.New("submission artifact is not ready")
	ErrSubmissionArtifactInvalid           = errors.New("submission artifact is invalid")
	ErrSubmissionQueueProvision            = errors.New("submission queue provisioning failed")
	ErrSubmissionIdentityPersist           = errors.New("submission identity persistence failed")
	ErrSubmissionIDGeneration              = errors.New("submission id generation failed")
	ErrSubmissionStorageCatalogUnavailable = errors.New("storage catalogue is unavailable")
	ErrSubmissionStorageAssetNotAllowed    = errors.New("storage asset is not allowed")
	ErrSubmissionStorageAssetKindInvalid   = errors.New("storage asset kind is invalid")
	ErrSubmissionStorageOutputNotWritable  = errors.New("storage output asset is not writable")
	ErrSubmissionDataSpacesUnavailable     = errors.New("data spaces are unavailable")
	ErrSubmissionDataMountNotReady         = errors.New("data-space mount is not ready")
	ErrSubmissionWorkspaceSnapshotNotFound = errors.New("workspace snapshot was not found")
	ErrSubmissionResumeCheckpointNotFound  = errors.New("resume checkpoint was not found")
	ErrSubmissionDatasetFeatureDisabled    = errors.New("versioned streaming datasets are disabled")
	ErrSubmissionDatasetNotFound           = errors.New("dataset was not found")
	ErrSubmissionDatasetVersionNotReady    = errors.New("dataset version is not ready")
	ErrSubmissionDatasetCatalogUnavailable = errors.New("dataset catalogue is unavailable")
	ErrSubmissionDatasetIncompatible       = errors.New("dataset is incompatible with the selected runtime")
	ErrSubmissionDatasetManifestInvalid    = errors.New("dataset manifest is invalid")
	ErrSubmissionDatasetInternalPath       = errors.New("internal dataset paths are platform-managed")
)

type SourceArtifactLookup interface {
	GetSourceArtifact(context.Context, string, string, string) (*domain.SourceArtifact, error)
}

// WorkspaceSnapshotLookup keeps source selection owner-scoped. A job stores
// only the opaque snapshot ID; the renderer uses the already-authorized
// personal PVC captured in ResolvedDataRoots.
type WorkspaceSnapshotLookup interface {
	GetWorkspaceSnapshot(context.Context, string, string, string) (*domain.WorkspaceSnapshot, error)
}

// TenantRuntimeEnsurer reconstructs the Kubernetes resources needed by a
// tenant before accepting its work. It makes a database-only tenant restored
// after a platform reset runnable again without a manual kubectl step.
type TenantRuntimeEnsurer func(context.Context, string, string, string, string) error

type SubmissionServiceOptions struct {
	// Images is the administrator-managed catalogue. When it holds any entry
	// for the tenant it becomes the authoritative allowlist; ImageAllowlist is
	// the fallback for deployments that have not populated it yet.
	Images                   ImageStore
	ImageAllowlist           []string
	RuntimePolicy            runtimecatalog.Policy
	GitAllowlist             []string
	ClusterQueue             string
	EnsureTenantRuntime      TenantRuntimeEnsurer
	EnsureDataSpaces         func(context.Context, auth.Principal) error
	EnsureOutputDirectory    func(context.Context, auth.Principal, domain.ResolvedDataSpaceMounts) error
	StorageAssets            StorageAssetStore
	DataSpaces               DataSpaceStore
	DataSpacesEnabled        bool
	DataSpacesPublicRoot     string
	IDCDataSpacesEnabled     bool
	WorkspaceSnapshots       WorkspaceSnapshotLookup
	Datasets                 DatasetCatalogStore
	DatasetVersioningEnabled bool
	RayDataStreamingEnabled  bool
	DatasetInternalPrefix    string
	NewID                    func() (string, error)
	LocalCache               LocalCachePolicy
}

type LocalCachePolicy struct {
	Enabled      bool
	AllowedSizes []string
	DefaultSize  string
	MaxSize      string
	MountPath    string
	MountPaths   []string
}

type SubmissionService struct {
	repository               JobRepository
	images                   ImageStore
	imageAllowlist           []string
	runtimePolicy            runtimecatalog.Policy
	gitAllowlist             []string
	clusterQueue             string
	ensureTenantRuntime      TenantRuntimeEnsurer
	ensureDataSpaces         func(context.Context, auth.Principal) error
	ensureOutputDirectory    func(context.Context, auth.Principal, domain.ResolvedDataSpaceMounts) error
	storageAssets            StorageAssetStore
	dataSpaces               DataSpaceStore
	dataSpacesEnabled        bool
	dataSpacesPublicRoot     string
	idcDataSpacesEnabled     bool
	workspaceSnapshots       WorkspaceSnapshotLookup
	datasets                 DatasetCatalogStore
	datasetVersioningEnabled bool
	rayDataStreamingEnabled  bool
	datasetInternalPrefix    string
	newID                    func() (string, error)
	localCache               LocalCachePolicy
}

type SubmissionInput struct {
	Principal            auth.Principal
	Spec                 domain.JobSpec
	Origin               domain.SubmissionOrigin
	IdempotencyKey       string
	ExternalSubmissionID string
}

// DatasetPreflightSummary is deliberately limited to logical, immutable
// metadata. Object-store keys and credentials never cross the API boundary.
type DatasetPreflightSummary struct {
	DatasetID      string                    `json:"datasetId"`
	DatasetSlug    string                    `json:"datasetSlug"`
	VersionID      string                    `json:"versionId"`
	Version        string                    `json:"version"`
	ManifestSHA256 string                    `json:"manifestSha256"`
	SchemaVersion  string                    `json:"schemaVersion"`
	TrainSamples   int64                     `json:"trainSamples"`
	ValSamples     int64                     `json:"valSamples"`
	TestSamples    int64                     `json:"testSamples"`
	LogicalBytes   int64                     `json:"logicalBytes"`
	PackedBytes    int64                     `json:"packedBytes"`
	DataMode       domain.DataMode           `json:"dataMode"`
	CachePolicy    domain.DatasetCachePolicy `json:"cachePolicy"`
}

type SubmissionPreflightResult struct {
	Image          string                   `json:"image"`
	TrainingEngine domain.TrainingEngine    `json:"trainingEngine"`
	RayVersion     string                   `json:"rayVersion"`
	RequestedGPUs  int                      `json:"requestedGpus"`
	Dataset        *DatasetPreflightSummary `json:"dataset,omitempty"`
}

type preparedSubmission struct {
	spec               domain.JobSpec
	resumeCheckpointID string
	dataset            domain.DatasetProvenance
	datasetSummary     *DatasetPreflightSummary
}

func NewSubmissionService(repository JobRepository, options SubmissionServiceOptions) *SubmissionService {
	newID := options.NewID
	if newID == nil {
		newID = newJobID
	}
	datasetInternalPrefix := strings.TrimSuffix(strings.TrimSpace(options.DatasetInternalPrefix), "/")
	if datasetInternalPrefix == "" {
		datasetInternalPrefix = internalDatasetObjectPrefix
	}
	return &SubmissionService{
		repository:               repository,
		images:                   options.Images,
		imageAllowlist:           append([]string(nil), options.ImageAllowlist...),
		runtimePolicy:            options.RuntimePolicy.Clone(),
		gitAllowlist:             append([]string(nil), options.GitAllowlist...),
		clusterQueue:             strings.TrimSpace(options.ClusterQueue),
		ensureTenantRuntime:      options.EnsureTenantRuntime,
		ensureDataSpaces:         options.EnsureDataSpaces,
		ensureOutputDirectory:    options.EnsureOutputDirectory,
		storageAssets:            options.StorageAssets,
		dataSpaces:               options.DataSpaces,
		dataSpacesEnabled:        options.DataSpacesEnabled,
		dataSpacesPublicRoot:     strings.TrimSpace(options.DataSpacesPublicRoot),
		idcDataSpacesEnabled:     options.IDCDataSpacesEnabled,
		workspaceSnapshots:       options.WorkspaceSnapshots,
		datasets:                 options.Datasets,
		datasetVersioningEnabled: options.DatasetVersioningEnabled,
		rayDataStreamingEnabled:  options.RayDataStreamingEnabled,
		datasetInternalPrefix:    datasetInternalPrefix,
		newID:                    newID,
		localCache:               cloneLocalCachePolicy(options.LocalCache),
	}
}

func cloneLocalCachePolicy(policy LocalCachePolicy) LocalCachePolicy {
	return LocalCachePolicy{
		Enabled:      policy.Enabled,
		AllowedSizes: append([]string(nil), policy.AllowedSizes...),
		DefaultSize:  strings.TrimSpace(policy.DefaultSize),
		MaxSize:      strings.TrimSpace(policy.MaxSize),
		MountPath:    strings.TrimSpace(policy.MountPath),
		MountPaths:   append([]string(nil), policy.MountPaths...),
	}
}

// resolveRuntime resolves the selected catalog entry once and snapshots its
// authoritative image, engine and Ray version before JobSpec validation. A
// deployment without catalog metadata retains only the legacy allowlist path.
func (service *SubmissionService) resolveRuntime(ctx context.Context, tenantID string, spec domain.JobSpec) (domain.JobSpec, error) {
	reference := strings.TrimSpace(spec.Image)
	if service.images != nil {
		catalog, err := service.images.ListImages(ctx, tenantID, domain.ImageKindTraining)
		if err != nil {
			return domain.JobSpec{}, ErrSubmissionImageNotAllowed
		}
		for _, image := range catalog {
			if image.Reference != reference {
				continue
			}
			effectivePolicy := service.runtimePolicy.EffectiveForTenant(tenantID)
			snapshot, resolveErr := runtimecatalog.Resolve(image, spec.TrainingEngine, effectivePolicy)
			if resolveErr != nil {
				return domain.JobSpec{}, ErrSubmissionImageNotAllowed
			}
			spec.Image = snapshot.ImageDigest
			spec.TrainingEngine = snapshot.Engine
			spec.RayVersion = snapshot.RayVersion
			return spec, nil
		}
		if len(catalog) > 0 {
			return domain.JobSpec{}, ErrSubmissionImageNotAllowed
		}
	}
	if !matchesAllowlist(reference, service.imageAllowlist) || spec.TrainingEngine.Resolved() != domain.TrainingEngineRayDDP {
		return domain.JobSpec{}, ErrSubmissionImageNotAllowed
	}
	spec.Image = reference
	spec.TrainingEngine = domain.TrainingEngineRayDDP
	spec.RayVersion = domain.RayVersionLegacy
	return spec, nil
}

func (service *SubmissionService) Preflight(ctx context.Context, input SubmissionInput) (SubmissionPreflightResult, error) {
	prepared, err := service.prepareSubmission(ctx, input)
	if err != nil {
		return SubmissionPreflightResult{}, err
	}
	return SubmissionPreflightResult{
		Image: prepared.spec.Image, TrainingEngine: prepared.spec.TrainingEngine,
		RayVersion:    prepared.spec.RayVersion,
		RequestedGPUs: prepared.spec.Resources.WorkerReplicas * prepared.spec.Resources.GPUsPerWorker,
		Dataset:       prepared.datasetSummary,
	}, nil
}

func (service *SubmissionService) prepareSubmission(ctx context.Context, input SubmissionInput) (preparedSubmission, error) {
	if service == nil || service.repository == nil {
		return preparedSubmission{}, fmt.Errorf("submission service is not configured")
	}
	if err := input.Origin.Validate(); err != nil {
		return preparedSubmission{}, fmt.Errorf("%w: %v", ErrSubmissionInvalidOrigin, err)
	}
	resolvedSpec, err := service.resolveRuntime(ctx, input.Principal.TenantID, input.Spec)
	if err != nil {
		return preparedSubmission{}, err
	}
	if !resolvedSpec.DatasetRef.IsZero() && (resolvedSpec.DataMode != domain.DataModeStreaming ||
		resolvedSpec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain || resolvedSpec.RayVersion != domain.RayVersionCanary) {
		return preparedSubmission{}, ErrSubmissionDatasetIncompatible
	}
	spec, err := normalizeSubmissionSpec(input.Principal, input.Origin, resolvedSpec, service.localCache)
	if err != nil {
		return preparedSubmission{}, err
	}
	resumeCheckpointID, err := service.resolveResumeCheckpoint(ctx, input.Principal, spec)
	if err != nil {
		return preparedSubmission{}, err
	}
	if spec.Source.Type == "git" && !matchesGitAllowlist(spec.Source.URL, service.gitAllowlist) {
		return preparedSubmission{}, ErrSubmissionGitNotAllowed
	}
	if spec.Source.Type == "workspace-archive" {
		var materializeErr error
		spec, materializeErr = service.materializeArtifact(ctx, input.Principal, spec)
		if materializeErr != nil {
			return preparedSubmission{}, materializeErr
		}
	}
	if spec.Source.Type == "workspace" {
		if err := service.authorizeWorkspaceSnapshot(ctx, input.Principal, spec.Source.Snapshot); err != nil {
			return preparedSubmission{}, err
		}
	}
	dataset, summary, err := service.resolveDatasetSnapshot(ctx, input.Principal, &spec)
	if err != nil {
		return preparedSubmission{}, err
	}
	return preparedSubmission{spec: spec, resumeCheckpointID: resumeCheckpointID, dataset: dataset, datasetSummary: summary}, nil
}

func (service *SubmissionService) Submit(ctx context.Context, input SubmissionInput) (*domain.TrainingJob, error) {
	prepared, err := service.prepareSubmission(ctx, input)
	if err != nil {
		return nil, err
	}
	spec := prepared.spec
	deferIdentityPersistence := spec.DataMode == domain.DataModeRayData || spec.DataMode == domain.DataModeRayDataStage || spec.DataMode == domain.DataModeStreaming
	if !deferIdentityPersistence {
		if err := service.repository.EnsureIdentity(ctx, input.Principal); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionIdentityPersist, err)
		}
	}
	if service.ensureTenantRuntime != nil && !deferIdentityPersistence {
		namespace := "tenant-" + sanitizeDNS(input.Principal.TenantID)
		if err := service.ensureTenantRuntime(ctx, input.Principal.TenantID, namespace, spec.Queue, service.clusterQueue); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionQueueProvision, err)
		}
	}
	if service.ensureDataSpaces != nil && !deferIdentityPersistence {
		if err := service.ensureDataSpaces(ctx, input.Principal); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionDataSpacesUnavailable, err)
		}
	}
	id, err := service.newID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmissionIDGeneration, err)
	}
	resolvedStorage, err := service.resolveStorageSelections(ctx, input.Principal, spec, id)
	if err != nil {
		return nil, err
	}
	spec.ResolvedStorage = resolvedStorage
	resolvedDataMounts, err := service.resolveLogicalDataMounts(ctx, input.Principal, spec, id)
	if err != nil {
		return nil, err
	}
	spec.ResolvedDataMounts = resolvedDataMounts
	if err := spec.Managed.ValidateResolvedDataMode(spec.DataMode, spec.ResolvedDataMounts); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmissionDataMountNotReady, err)
	}
	if service.ensureOutputDirectory != nil && spec.ResolvedDataMounts.Output != nil {
		if err := service.ensureOutputDirectory(ctx, input.Principal, spec.ResolvedDataMounts); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionDataMountNotReady, err)
		}
	}
	resolvedDataRoots, err := service.resolveDataSpaceRoots(ctx, input.Principal)
	if err != nil {
		return nil, err
	}
	spec.ResolvedDataRoots = resolvedDataRoots
	if (spec.Source.Type == "workspace" || spec.Source.Type == "workspace-archive") && spec.ResolvedDataRoots.Personal == nil {
		return nil, ErrSubmissionDataMountNotReady
	}
	if deferIdentityPersistence {
		if service.ensureTenantRuntime != nil {
			namespace := "tenant-" + sanitizeDNS(input.Principal.TenantID)
			if err := service.ensureTenantRuntime(ctx, input.Principal.TenantID, namespace, spec.Queue, service.clusterQueue); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrSubmissionQueueProvision, err)
			}
		}
		if err := service.repository.EnsureIdentity(ctx, input.Principal); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionIdentityPersist, err)
		}
		if service.ensureDataSpaces != nil {
			if err := service.ensureDataSpaces(ctx, input.Principal); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrSubmissionDataSpacesUnavailable, err)
			}
		}
	}
	job := &domain.TrainingJob{
		ID: id, TenantID: input.Principal.TenantID, UserID: input.Principal.Subject,
		Spec: spec, DesiredState: domain.DesiredActive, ObservedState: domain.StateSubmitted,
		DatasetProvenance: prepared.dataset,
		KubernetesNS:      "tenant-" + sanitizeDNS(input.Principal.TenantID),
		SubmissionOrigin:  input.Origin, ExternalSubmissionID: strings.TrimSpace(input.ExternalSubmissionID),
		ResumeCheckpointID: prepared.resumeCheckpointID,
	}
	if spec.Source.Type == "workspace-archive" {
		job.SourceArtifactID = spec.Source.ArtifactID
	}
	if err := service.repository.Create(ctx, job, input.IdempotencyKey); err != nil {
		if errors.Is(err, repositories.ErrDatasetSnapshotConflict) {
			return nil, ErrSubmissionDatasetVersionNotReady
		}
		return nil, err
	}
	return job, nil
}

func (service *SubmissionService) resolveDatasetSnapshot(ctx context.Context, principal auth.Principal, spec *domain.JobSpec) (domain.DatasetProvenance, *DatasetPreflightSummary, error) {
	if spec == nil {
		return domain.DatasetProvenance{}, nil, fmt.Errorf("%w: missing job spec", ErrSubmissionInvalidJobSpec)
	}
	if service.referencesInternalDatasetPath(*spec) {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetInternalPath
	}
	datasetRef := spec.DatasetRef
	if datasetRef.IsZero() {
		if spec.DataMode == domain.DataModeStreaming {
			return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetNotFound
		}
		return domain.DatasetProvenance{}, nil, nil
	}
	if !service.datasetVersioningEnabled || !service.rayDataStreamingEnabled {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetFeatureDisabled
	}
	if spec.DataMode != domain.DataModeStreaming || spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain || spec.RayVersion != domain.RayVersionCanary {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetIncompatible
	}
	if service.datasets == nil {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetNotFound
	}
	if hasDatasetPathOverride(*spec) {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetInternalPath
	}

	datasetToken, selector, err := parseDatasetReference(datasetRef)
	if err != nil {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetNotFound
	}
	superAdmin := principal.HasRole(domain.RoleSuperAdmin)
	datasets, err := service.datasets.ListDatasets(ctx, principal.TenantID, superAdmin)
	if err != nil {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetCatalogUnavailable
	}
	dataset, err := selectVisibleDataset(datasets, datasetToken)
	if err != nil || (dataset.Visibility == domain.DatasetVisibilityTeam && dataset.OwnerTenantID != principal.TenantID) {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetNotFound
	}
	if err := dataset.Validate(); err != nil || !domain.CanViewDataset(dataset, principal.TenantID, superAdmin) {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetIncompatible
	}
	version, err := service.datasets.ResolveReadyDatasetVersion(ctx, principal.TenantID, superAdmin, dataset.ID, selector)
	if err != nil {
		if errors.Is(err, repositories.ErrDatasetVersionNotReady) || errors.Is(err, repositories.ErrDatasetVersionNotFound) {
			return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetVersionNotReady
		}
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetCatalogUnavailable
	}
	if version.State != domain.DatasetVersionReady {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetVersionNotReady
	}
	if version.DatasetID != dataset.ID || version.SchemaVersion != dataset.SchemaVersion {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetIncompatible
	}
	if err := version.ValidateWithInternalPrefix(service.datasetInternalPrefix); err != nil || !service.validInternalManifest(version) {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetManifestInvalid
	}

	provenance := domain.DatasetProvenance{
		DatasetID: dataset.ID, DatasetVersionID: version.ID, ManifestSHA256: version.ManifestSHA256,
		DataMode: domain.DataModeStreaming, CachePolicy: spec.CachePolicy,
	}
	if err := provenance.Validate(); err != nil {
		return domain.DatasetProvenance{}, nil, ErrSubmissionDatasetManifestInvalid
	}
	// latest is only an input selector. Persist a concrete immutable reference.
	spec.DatasetRef = domain.DatasetReference{Dataset: dataset.ID, Version: version.ID}
	summary := &DatasetPreflightSummary{
		DatasetID: dataset.ID, DatasetSlug: dataset.Slug, VersionID: version.ID, Version: version.Version,
		ManifestSHA256: version.ManifestSHA256, SchemaVersion: version.SchemaVersion,
		TrainSamples: version.TrainSamples, ValSamples: version.ValSamples, TestSamples: version.TestSamples,
		LogicalBytes: version.LogicalBytes, PackedBytes: version.PackedBytes,
		DataMode: domain.DataModeStreaming, CachePolicy: spec.CachePolicy,
	}
	return provenance, summary, nil
}

func parseDatasetReference(ref domain.DatasetReference) (string, domain.DatasetVersionSelector, error) {
	if err := ref.Validate(); err != nil || ref.IsZero() {
		return "", domain.DatasetVersionSelector{}, fmt.Errorf("invalid dataset reference")
	}
	selector, err := domain.ParseDatasetVersionSelector(ref.Version)
	if err != nil {
		return "", domain.DatasetVersionSelector{}, err
	}
	return ref.Dataset, selector, nil
}

func selectVisibleDataset(datasets []domain.Dataset, token string) (domain.Dataset, error) {
	for _, dataset := range datasets {
		if dataset.ID == token {
			return dataset, nil
		}
	}
	var selected domain.Dataset
	matches := 0
	for _, dataset := range datasets {
		if dataset.Slug == token {
			selected = dataset
			matches++
		}
	}
	if matches != 1 {
		return domain.Dataset{}, fmt.Errorf("dataset reference is missing or ambiguous")
	}
	return selected, nil
}

func hasDatasetPathOverride(spec domain.JobSpec) bool {
	return strings.TrimSpace(spec.DatasetURI) != "" || spec.DatasetStorage != (domain.StorageSelection{}) ||
		spec.Input != (domain.DataLocation{}) || !spec.Managed.RayData.IsZero() || spec.Cache != (domain.CacheRequest{})
}

func (service *SubmissionService) referencesInternalDatasetPath(spec domain.JobSpec) bool {
	prefix := strings.ToLower(strings.Trim(service.datasetInternalPrefix, "/"))
	if prefix == "" {
		return false
	}
	values := []string{spec.DatasetURI, spec.DatasetStorage.RelativePath, spec.Input.RelativePath, spec.DatasetRef.Dataset, spec.DatasetRef.Version}
	for _, value := range values {
		candidate := strings.ToLower(strings.TrimSpace(value))
		for attempts := 0; attempts < 16; attempts++ {
			if strings.Contains(candidate, prefix) {
				return true
			}
			decoded, err := url.PathUnescape(candidate)
			if err != nil || decoded == candidate {
				break
			}
			candidate = decoded
		}
		// The sixteenth decode may be the one that reveals the clear-text
		// prefix. Check the boundary value before deciding whether another
		// decoding layer exists.
		if strings.Contains(candidate, prefix) {
			return true
		}
		// More than sixteen decoding layers are not meaningful object paths.
		// Reject them fail-closed instead of allowing a downstream decoder to
		// reveal an internal prefix after this boundary.
		if decoded, err := url.PathUnescape(candidate); err == nil && decoded != candidate {
			return true
		}
	}
	return false
}

func (service *SubmissionService) validInternalManifest(version domain.DatasetVersion) bool {
	prefix := strings.Trim(service.datasetInternalPrefix, "/")
	expected := path.Join(prefix, version.DatasetID, "manifests", version.ID+".parquet")
	return version.ManifestObjectKey == expected && len(version.ManifestSHA256) == 64
}

func (service *SubmissionService) resolveResumeCheckpoint(ctx context.Context, principal auth.Principal, spec domain.JobSpec) (string, error) {
	if spec.ParentJobID == "" {
		return "", nil
	}
	if spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain || spec.Checkpoint.Space != domain.DataSpaceMyRuns {
		return "", ErrSubmissionResumeCheckpointNotFound
	}
	parent, err := service.repository.Get(ctx, principal.TenantID, spec.ParentJobID)
	if err != nil || parent == nil || parent.ID != spec.ParentJobID || parent.TenantID != principal.TenantID || parent.UserID != principal.Subject || parent.Spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain || parent.Spec.Output.Space != domain.DataSpaceMyRuns {
		return "", ErrSubmissionResumeCheckpointNotFound
	}
	store, ok := service.repository.(checkpointStore)
	if !ok {
		return "", ErrSubmissionResumeCheckpointNotFound
	}
	checkpoints, err := store.ListUsableCheckpoints(ctx, parent.TenantID, parent.UserID, parent.ID)
	if err != nil {
		return "", ErrSubmissionResumeCheckpointNotFound
	}
	for _, checkpoint := range checkpoints {
		if !checkpoint.Complete || checkpoint.JobID != parent.ID || checkpoint.TenantID != parent.TenantID || checkpoint.UserID != parent.UserID || checkpoint.Validate() != nil {
			continue
		}
		expectedObjectPath := path.Join(domain.DataMountOutputPath, ".platform", "ray-train", parent.ID, "checkpoints", checkpoint.ID)
		expectedRelativePath := path.Join(strings.Trim(parent.Spec.Output.RelativePath, "/"), parent.ID, ".platform", "ray-train", parent.ID, "checkpoints", checkpoint.ID)
		if checkpoint.ObjectPath == expectedObjectPath && spec.Checkpoint.RelativePath == expectedRelativePath {
			return checkpoint.ID, nil
		}
	}
	return "", ErrSubmissionResumeCheckpointNotFound
}

func (service *SubmissionService) authorizeWorkspaceSnapshot(ctx context.Context, principal auth.Principal, snapshotID string) error {
	if service.workspaceSnapshots == nil {
		return ErrSubmissionWorkspaceSnapshotNotFound
	}
	snapshot, err := service.workspaceSnapshots.GetWorkspaceSnapshot(ctx, principal.TenantID, principal.Subject, snapshotID)
	if err != nil || snapshot == nil {
		return ErrSubmissionWorkspaceSnapshotNotFound
	}
	if err := snapshot.Validate(); err != nil || snapshot.ID != snapshotID || snapshot.TenantID != principal.TenantID || snapshot.UserID != principal.Subject {
		return ErrSubmissionWorkspaceSnapshotNotFound
	}
	return nil
}

func (service *SubmissionService) resolveLogicalDataMounts(ctx context.Context, principal auth.Principal, spec domain.JobSpec, jobID string) (domain.ResolvedDataSpaceMounts, error) {
	if !hasLogicalLocations(spec) {
		return domain.ResolvedDataSpaceMounts{}, nil
	}
	if !service.dataSpacesEnabled && !service.idcDataSpacesEnabled {
		return domain.ResolvedDataSpaceMounts{}, ErrSubmissionDataMountNotReady
	}
	if service.dataSpaces == nil {
		return domain.ResolvedDataSpaceMounts{}, ErrSubmissionDataSpacesUnavailable
	}
	bindings, err := service.dataSpaces.ListDataBindings(ctx, principal.TenantID, principal.Subject)
	if err != nil {
		return domain.ResolvedDataSpaceMounts{}, ErrSubmissionDataSpacesUnavailable
	}
	ready := make(map[domain.DataSpaceID]domain.DataMountBinding, len(bindings))
	for _, binding := range bindings {
		if binding.Status != domain.DataMountBindingReady || !bindingVisibleToPrincipal(binding, principal) || !service.dataSpaceMountingEnabled(binding.SpaceID) {
			continue
		}
		ready[binding.SpaceID] = binding
	}
	tenantRoot := ready[domain.DataSpaceTenantStorageRoot]
	publicRoot, err := service.publicDataRootForTenant(principal.TenantID)
	if err != nil {
		return domain.ResolvedDataSpaceMounts{}, ErrSubmissionDataMountNotReady
	}
	input, err := resolveLogicalDataMount(spec.Input, ready, tenantRoot, publicRoot, domain.DataMountInputPath, true, "")
	if err != nil {
		return domain.ResolvedDataSpaceMounts{}, err
	}
	checkpoint, err := resolveLogicalDataMount(spec.Checkpoint, ready, tenantRoot, publicRoot, domain.DataMountCheckpointPath, true, "")
	if err != nil {
		return domain.ResolvedDataSpaceMounts{}, err
	}
	output, err := resolveLogicalDataMount(spec.Output, ready, tenantRoot, publicRoot, domain.DataMountOutputPath, false, joinDataSubPath("runs", joinDataSubPath(spec.Output.RelativePath, jobID)))
	if err != nil {
		return domain.ResolvedDataSpaceMounts{}, err
	}
	return domain.ResolvedDataSpaceMounts{Input: input, Checkpoint: checkpoint, Output: output}, nil
}

func (service *SubmissionService) dataSpaceMountingEnabled(space domain.DataSpaceID) bool {
	switch space {
	case domain.DataSpaceIDCOriginal, domain.DataSpaceIDCWellspiking, domain.DataSpaceIDCShared:
		return service.idcDataSpacesEnabled
	default:
		return service.dataSpacesEnabled
	}
}

func hasLogicalLocations(spec domain.JobSpec) bool {
	return spec.Input.Space != "" || spec.Input.RelativePath != "" ||
		spec.Checkpoint.Space != "" || spec.Checkpoint.RelativePath != "" ||
		spec.Output.Space != "" || spec.Output.RelativePath != ""
}

func logicalLocationBindingSpace(space domain.DataSpaceID) domain.DataSpaceID {
	if space == domain.DataSpaceMyStorage || space == domain.DataSpaceMyFiles || space == domain.DataSpaceMyRuns {
		return domain.DataSpaceWorkspace
	}
	return space
}

func bindingVisibleToPrincipal(binding domain.DataMountBinding, principal auth.Principal) bool {
	switch binding.Scope {
	case domain.DataMountScopePersonal:
		return binding.TenantID == principal.TenantID && binding.UserID == principal.Subject && binding.SpaceID == domain.DataSpaceWorkspace
	case domain.DataMountScopeTenant, domain.DataMountScopeIDC:
		return binding.TenantID == principal.TenantID && binding.UserID == ""
	default:
		return false
	}
}

func resolveLogicalDataMount(location domain.DataLocation, ready map[domain.DataSpaceID]domain.DataMountBinding, tenantRoot domain.DataMountBinding, publicRoot, mountPath string, readOnly bool, generatedSubPath string) (*domain.ResolvedDataMount, error) {
	if location.Space == "" {
		return nil, nil
	}
	bindingSpace := logicalLocationBindingSpace(location.Space)
	binding, ok := ready[bindingSpace]
	if !ok || strings.TrimSpace(binding.ClaimName) == "" {
		return nil, ErrSubmissionDataMountNotReady
	}
	subPath := location.RelativePath
	if generatedSubPath != "" {
		if location.Space != domain.DataSpaceMyRuns {
			return nil, ErrSubmissionDataMountNotReady
		}
		subPath = generatedSubPath
	} else if location.Space == domain.DataSpaceMyFiles {
		subPath = joinDataSubPath("files", location.RelativePath)
	} else if location.Space == domain.DataSpaceMyRuns {
		subPath = joinDataSubPath("runs", location.RelativePath)
	}
	claimName := binding.ClaimName
	if isTOSWorkloadSpace(bindingSpace) && strings.TrimSpace(tenantRoot.ClaimName) != "" {
		logicalRoot := binding.RootPrefix
		if bindingSpace == domain.DataSpacePublic {
			logicalRoot = publicRoot
		}
		rootSubPath, err := dataRootSubPath(tenantRoot.RootPrefix, logicalRoot)
		if err != nil {
			return nil, ErrSubmissionDataMountNotReady
		}
		claimName = tenantRoot.ClaimName
		subPath = joinDataSubPath(rootSubPath, subPath)
	}
	mount := &domain.ResolvedDataMount{
		Space: location.Space, BindingSpace: bindingSpace, ClaimName: claimName,
		SubPath: subPath, MountPath: mountPath, ReadOnly: readOnly,
	}
	if err := (domain.ResolvedDataSpaceMounts{Input: mountForPath(mount, domain.DataMountInputPath), Checkpoint: mountForPath(mount, domain.DataMountCheckpointPath), Output: mountForPath(mount, domain.DataMountOutputPath)}).Validate(); err != nil {
		return nil, ErrSubmissionDataMountNotReady
	}
	return mount, nil
}

func joinDataSubPath(prefix, relativePath string) string {
	if prefix == "" {
		return relativePath
	}
	if relativePath == "" {
		return prefix
	}
	return prefix + "/" + relativePath
}

func mountForPath(mount *domain.ResolvedDataMount, path string) *domain.ResolvedDataMount {
	if mount != nil && mount.MountPath == path {
		return mount
	}
	return nil
}

func (service *SubmissionService) resolveDataSpaceRoots(ctx context.Context, principal auth.Principal) (domain.ResolvedDataSpaceRoots, error) {
	if service.dataSpaces == nil || (!service.dataSpacesEnabled && !service.idcDataSpacesEnabled) {
		return domain.ResolvedDataSpaceRoots{}, nil
	}
	bindings, err := service.dataSpaces.ListDataBindings(ctx, principal.TenantID, principal.Subject)
	if err != nil {
		return domain.ResolvedDataSpaceRoots{}, ErrSubmissionDataSpacesUnavailable
	}
	ready := make(map[domain.DataSpaceID]domain.DataMountBinding, len(bindings))
	for _, binding := range bindings {
		if binding.Status == domain.DataMountBindingReady && bindingVisibleToPrincipal(binding, principal) && service.dataSpaceMountingEnabled(binding.SpaceID) {
			ready[binding.SpaceID] = binding
		}
	}
	tenantRoot := ready[domain.DataSpaceTenantStorageRoot]
	publicRoot, err := service.publicDataRootForTenant(principal.TenantID)
	if err != nil {
		return domain.ResolvedDataSpaceRoots{}, ErrSubmissionDataMountNotReady
	}
	root := func(space domain.DataSpaceID, readOnly bool) *domain.ResolvedDataRoot {
		binding, ok := ready[space]
		if !ok || strings.TrimSpace(binding.ClaimName) == "" {
			return nil
		}
		if isTOSWorkloadSpace(space) && strings.TrimSpace(tenantRoot.ClaimName) != "" {
			logicalRoot := binding.RootPrefix
			if space == domain.DataSpacePublic {
				logicalRoot = publicRoot
			}
			subPath, err := dataRootSubPath(tenantRoot.RootPrefix, logicalRoot)
			if err != nil {
				return nil
			}
			return &domain.ResolvedDataRoot{Space: space, ClaimName: tenantRoot.ClaimName, SubPath: subPath, ReadOnly: readOnly}
		}
		return &domain.ResolvedDataRoot{Space: space, ClaimName: binding.ClaimName, ReadOnly: readOnly}
	}
	roots := domain.ResolvedDataSpaceRoots{
		Personal: root(domain.DataSpaceWorkspace, false), Team: root(domain.DataSpaceTeamShared, true), Public: root(domain.DataSpacePublic, true),
		IDCOriginal: root(domain.DataSpaceIDCOriginal, true), IDCWellspiking: root(domain.DataSpaceIDCWellspiking, true), IDCShared: root(domain.DataSpaceIDCShared, true),
	}
	if service.idcDataSpacesEnabled && (roots.IDCOriginal == nil || roots.IDCWellspiking == nil || roots.IDCShared == nil) {
		return domain.ResolvedDataSpaceRoots{}, ErrSubmissionDataMountNotReady
	}
	if err := roots.Validate(); err != nil {
		return domain.ResolvedDataSpaceRoots{}, ErrSubmissionDataMountNotReady
	}
	return roots, nil
}

func (service *SubmissionService) publicDataRootForTenant(tenantID string) (string, error) {
	root := service.dataSpacesPublicRoot
	if root == "" {
		root = domain.DefaultPublicDataRoot
	}
	return domain.PublicDataRootForTenant(tenantID, root)
}

func normalizeSubmissionSpec(principal auth.Principal, origin domain.SubmissionOrigin, spec domain.JobSpec, cachePolicy LocalCachePolicy) (domain.JobSpec, error) {
	// Claim names and mount paths are server-generated. Clear a value supplied
	// by an API client before validation and storage resolution so it cannot be
	// used to mount an arbitrary PVC.
	spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{}
	spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{}
	spec.DatasetRef = domain.DatasetReference{
		Dataset: strings.TrimSpace(spec.DatasetRef.Dataset),
		Version: strings.TrimSpace(spec.DatasetRef.Version),
	}
	spec.CachePolicy = domain.DatasetCachePolicy(strings.TrimSpace(string(spec.CachePolicy)))
	if !spec.DatasetRef.IsZero() && spec.CachePolicy == "" {
		spec.CachePolicy = domain.DatasetCachePolicyAuto
	}
	if spec.DataMode == domain.DataModeRayData || spec.DataMode == domain.DataModeRayDataStage || spec.DataMode == domain.DataModeStreaming {
		if err := spec.Managed.ValidateDataMode(spec.DataMode); err != nil {
			return domain.JobSpec{}, fmt.Errorf("%w: %v", ErrSubmissionInvalidJobSpec, err)
		}
		if spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain {
			return domain.JobSpec{}, fmt.Errorf("%w: %s requires ray-train", ErrSubmissionInvalidJobSpec, spec.DataMode)
		}
	}
	cache, err := normalizeDataModeCacheRequest(spec.DataMode, spec.Cache, spec.CachePolicy, cachePolicy)
	if err != nil {
		return domain.JobSpec{}, fmt.Errorf("%w: %v", ErrSubmissionInvalidJobSpec, err)
	}
	spec.Cache = cache
	expectedQueue := tenantQueue(principal.TenantID)
	if spec.Queue == "" {
		spec.Queue = expectedQueue
	} else if spec.Queue != expectedQueue {
		return domain.JobSpec{}, ErrSubmissionQueueNotAllowed
	}
	if err := spec.Validate(); err != nil {
		return domain.JobSpec{}, fmt.Errorf("%w: %v", ErrSubmissionInvalidJobSpec, err)
	}
	if spec.Source.Type != "git" && spec.Source.Type != "workspace" && !(spec.Source.Type == "workspace-archive" && origin == domain.SubmissionOriginRayCLI) {
		return domain.JobSpec{}, ErrSubmissionCodeSourceNotAllowed
	}
	return spec, nil
}

func normalizeDataModeCacheRequest(mode domain.DataMode, cache domain.CacheRequest, datasetCachePolicy domain.DatasetCachePolicy, policy LocalCachePolicy) (domain.CacheRequest, error) {
	if mode == domain.DataModeStreaming {
		if cache != (domain.CacheRequest{}) {
			return domain.CacheRequest{}, fmt.Errorf("streaming cache is selected through cachePolicy")
		}
		if datasetCachePolicy == domain.DatasetCachePolicyBounded {
			if !policy.Enabled {
				return domain.CacheRequest{}, fmt.Errorf("streaming runtime cache capability is disabled")
			}
			if !hasDualLocalCacheMounts(policy) {
				return domain.CacheRequest{}, fmt.Errorf("streaming requires dual-NVMe runtime cache capability")
			}
		}
		return domain.CacheRequest{}, nil
	}
	if mode != domain.DataModeRayData && mode != domain.DataModeRayDataStage {
		return normalizeCacheRequest(cache, policy)
	}
	if !policy.Enabled {
		return domain.CacheRequest{}, fmt.Errorf("%s runtime cache capability is disabled", mode)
	}
	if !hasDualLocalCacheMounts(policy) {
		return domain.CacheRequest{}, fmt.Errorf("%s requires dual-NVMe runtime cache capability", mode)
	}

	cache.Mode = domain.CacheMode(strings.TrimSpace(string(cache.Mode)))
	cache.Size = strings.TrimSpace(cache.Size)
	cache.Preload = domain.CachePreloadMode(strings.TrimSpace(string(cache.Preload)))
	if cache.Mode == "" {
		cache.Mode = domain.CacheModeRuntime
	}
	if cache.Mode != domain.CacheModeRuntime {
		return domain.CacheRequest{}, fmt.Errorf("%s requires runtime cache", mode)
	}
	if cache.Preload != "" {
		return domain.CacheRequest{}, fmt.Errorf("%s mode does not support cache preload", mode)
	}
	return normalizeCacheRequest(cache, policy)
}

func hasDualLocalCacheMounts(policy LocalCachePolicy) bool {
	if len(policy.MountPaths) < 2 {
		return false
	}
	first := strings.TrimSpace(policy.MountPaths[0])
	second := strings.TrimSpace(policy.MountPaths[1])
	return first != "" && second != "" && first != second
}

func normalizeCacheRequest(cache domain.CacheRequest, policy LocalCachePolicy) (domain.CacheRequest, error) {
	cache.Mode = domain.CacheMode(strings.TrimSpace(string(cache.Mode)))
	cache.Size = strings.TrimSpace(cache.Size)
	cache.Preload = domain.CachePreloadMode(strings.TrimSpace(string(cache.Preload)))
	if cache.Mode == "" {
		if err := cache.Validate(); err != nil {
			return domain.CacheRequest{}, err
		}
		return cache, nil
	}
	if cache.Mode != domain.CacheModeRuntime {
		if err := cache.Validate(); err != nil {
			return domain.CacheRequest{}, err
		}
		return cache, nil
	}
	if !policy.Enabled {
		return domain.CacheRequest{}, fmt.Errorf("runtime cache capability is disabled")
	}
	if cache.Size == "" {
		cache.Size = strings.TrimSpace(policy.DefaultSize)
	}
	if err := cache.Validate(); err != nil {
		return domain.CacheRequest{}, err
	}
	requested, _ := resource.ParseQuantity(cache.Size)
	maximum, err := resource.ParseQuantity(strings.TrimSpace(policy.MaxSize))
	if err != nil || maximum.Sign() <= 0 {
		return domain.CacheRequest{}, fmt.Errorf("runtime cache maximum size is invalid")
	}
	if requested.Cmp(maximum) > 0 {
		return domain.CacheRequest{}, fmt.Errorf("runtime cache size %q exceeds maximum %q", cache.Size, policy.MaxSize)
	}
	for _, configured := range policy.AllowedSizes {
		allowed, err := resource.ParseQuantity(strings.TrimSpace(configured))
		if err == nil && allowed.Sign() > 0 && requested.Cmp(allowed) == 0 {
			cache.Size = strings.TrimSpace(configured)
			return cache, nil
		}
	}
	return domain.CacheRequest{}, fmt.Errorf("runtime cache size %q is not allowed", cache.Size)
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
		Name: spec.Name, Image: spec.Image, Source: materialized, Entrypoint: spec.Entrypoint, Execution: spec.Execution, Resources: spec.Resources,
		Queue: spec.Queue, Priority: spec.Priority, DatasetURI: spec.DatasetURI, CheckpointURI: spec.CheckpointURI,
		OutputURI: spec.OutputURI, DatasetStorage: spec.DatasetStorage, CheckpointStorage: spec.CheckpointStorage,
		OutputStorage: spec.OutputStorage, Input: spec.Input, Checkpoint: spec.Checkpoint, Output: spec.Output,
		ResolvedStorage: spec.ResolvedStorage, ResolvedDataMounts: spec.ResolvedDataMounts, ResolvedDataRoots: spec.ResolvedDataRoots, TimeoutSeconds: spec.TimeoutSeconds,
		RetryPolicy: spec.RetryPolicy, CleanupPolicy: spec.CleanupPolicy,
		TrainingEngine: spec.TrainingEngine, RayVersion: spec.RayVersion, Managed: spec.Managed,
		DataMode: spec.DataMode, DatasetRef: spec.DatasetRef, CachePolicy: spec.CachePolicy,
		ParentJobID: spec.ParentJobID, Cache: spec.Cache,
	}, nil
}

func (service *SubmissionService) resolveStorageSelections(ctx context.Context, principal auth.Principal, spec domain.JobSpec, jobID string) (domain.ResolvedStorageMounts, error) {
	if spec.DatasetStorage.AssetID == "" && spec.CheckpointStorage.AssetID == "" && spec.OutputStorage.AssetID == "" {
		return domain.ResolvedStorageMounts{}, nil
	}
	if service.storageAssets == nil {
		return domain.ResolvedStorageMounts{}, ErrSubmissionStorageCatalogUnavailable
	}
	dataset, err := service.resolveStorageSelection(ctx, principal, spec.DatasetStorage, domain.StorageAssetDataset, "")
	if err != nil {
		return domain.ResolvedStorageMounts{}, err
	}
	checkpoint, err := service.resolveStorageSelection(ctx, principal, spec.CheckpointStorage, domain.StorageAssetCheckpoint, "")
	if err != nil {
		return domain.ResolvedStorageMounts{}, err
	}
	output, err := service.resolveStorageSelection(ctx, principal, spec.OutputStorage, domain.StorageAssetOutput, "runs/"+jobID)
	if err != nil {
		return domain.ResolvedStorageMounts{}, err
	}
	return domain.ResolvedStorageMounts{Dataset: dataset, Checkpoint: checkpoint, Output: output}, nil
}

func (service *SubmissionService) resolveStorageSelection(ctx context.Context, principal auth.Principal, selection domain.StorageSelection, expectedKind, resolvedPath string) (*domain.ResolvedStorageMount, error) {
	if selection.AssetID == "" {
		return nil, nil
	}
	asset, err := service.storageAssets.GetStorageAsset(ctx, principal.TenantID, principal.Subject, selection.AssetID)
	if errors.Is(err, repositories.ErrStorageAssetNotFound) {
		return nil, ErrSubmissionStorageAssetNotAllowed
	}
	if err != nil {
		return nil, ErrSubmissionStorageCatalogUnavailable
	}
	if asset.Kind != expectedKind {
		return nil, ErrSubmissionStorageAssetKindInvalid
	}
	if expectedKind == domain.StorageAssetOutput && asset.ReadOnly {
		return nil, ErrSubmissionStorageOutputNotWritable
	}
	if expectedKind != domain.StorageAssetOutput && !asset.ReadOnly {
		return nil, ErrSubmissionStorageAssetKindInvalid
	}
	path := selection.RelativePath
	if resolvedPath != "" {
		path = resolvedPath
	}
	mount, err := asset.Resolve(path)
	if err != nil {
		return nil, ErrSubmissionStorageCatalogUnavailable
	}
	return &mount, nil
}
