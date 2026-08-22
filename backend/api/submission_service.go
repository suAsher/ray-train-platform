package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
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
	Images                ImageStore
	ImageAllowlist        []string
	GitAllowlist          []string
	ClusterQueue          string
	EnsureTenantRuntime   TenantRuntimeEnsurer
	EnsureDataSpaces      func(context.Context, auth.Principal) error
	EnsureOutputDirectory func(context.Context, auth.Principal, domain.ResolvedDataSpaceMounts) error
	StorageAssets         StorageAssetStore
	DataSpaces            DataSpaceStore
	DataSpacesEnabled     bool
	DataSpacesPublicRoot  string
	IDCDataSpacesEnabled  bool
	WorkspaceSnapshots    WorkspaceSnapshotLookup
	NewID                 func() (string, error)
}

type SubmissionService struct {
	repository            JobRepository
	images                ImageStore
	imageAllowlist        []string
	gitAllowlist          []string
	clusterQueue          string
	ensureTenantRuntime   TenantRuntimeEnsurer
	ensureDataSpaces      func(context.Context, auth.Principal) error
	ensureOutputDirectory func(context.Context, auth.Principal, domain.ResolvedDataSpaceMounts) error
	storageAssets         StorageAssetStore
	dataSpaces            DataSpaceStore
	dataSpacesEnabled     bool
	dataSpacesPublicRoot  string
	idcDataSpacesEnabled  bool
	workspaceSnapshots    WorkspaceSnapshotLookup
	newID                 func() (string, error)
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
		repository:            repository,
		images:                options.Images,
		imageAllowlist:        append([]string(nil), options.ImageAllowlist...),
		gitAllowlist:          append([]string(nil), options.GitAllowlist...),
		clusterQueue:          strings.TrimSpace(options.ClusterQueue),
		ensureTenantRuntime:   options.EnsureTenantRuntime,
		ensureDataSpaces:      options.EnsureDataSpaces,
		ensureOutputDirectory: options.EnsureOutputDirectory,
		storageAssets:         options.StorageAssets,
		dataSpaces:            options.DataSpaces,
		dataSpacesEnabled:     options.DataSpacesEnabled,
		dataSpacesPublicRoot:  strings.TrimSpace(options.DataSpacesPublicRoot),
		idcDataSpacesEnabled:  options.IDCDataSpacesEnabled,
		workspaceSnapshots:    options.WorkspaceSnapshots,
		newID:                 newID,
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
	spec, err := normalizeSubmissionSpec(input.Principal, input.Origin, input.Spec)
	if err != nil {
		return nil, err
	}
	if !service.imagePermitted(ctx, input.Principal.TenantID, spec.Image) {
		return nil, ErrSubmissionImageNotAllowed
	}
	if spec.Source.Type == "git" && !matchesGitAllowlist(spec.Source.URL, service.gitAllowlist) {
		return nil, ErrSubmissionGitNotAllowed
	}
	if spec.Source.Type == "workspace-archive" {
		var materializeErr error
		spec, materializeErr = service.materializeArtifact(ctx, input.Principal, spec)
		if materializeErr != nil {
			return nil, materializeErr
		}
	}
	if spec.Source.Type == "workspace" {
		if err := service.authorizeWorkspaceSnapshot(ctx, input.Principal, spec.Source.Snapshot); err != nil {
			return nil, err
		}
	}
	if err := service.repository.EnsureIdentity(ctx, input.Principal); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmissionIdentityPersist, err)
	}
	if service.ensureTenantRuntime != nil {
		namespace := "tenant-" + sanitizeDNS(input.Principal.TenantID)
		if err := service.ensureTenantRuntime(ctx, input.Principal.TenantID, namespace, spec.Queue, service.clusterQueue); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubmissionQueueProvision, err)
		}
	}
	if service.ensureDataSpaces != nil {
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
	job := &domain.TrainingJob{
		ID: id, TenantID: input.Principal.TenantID, UserID: input.Principal.Subject,
		Spec: spec, DesiredState: domain.DesiredActive, ObservedState: domain.StateSubmitted,
		KubernetesNS:     "tenant-" + sanitizeDNS(input.Principal.TenantID),
		SubmissionOrigin: input.Origin, ExternalSubmissionID: strings.TrimSpace(input.ExternalSubmissionID),
	}
	if spec.Source.Type == "workspace-archive" {
		job.SourceArtifactID = spec.Source.ArtifactID
	}
	if err := service.repository.Create(ctx, job, input.IdempotencyKey); err != nil {
		return nil, err
	}
	return job, nil
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
	if space == domain.DataSpaceMyFiles || space == domain.DataSpaceMyRuns {
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

func normalizeSubmissionSpec(principal auth.Principal, origin domain.SubmissionOrigin, spec domain.JobSpec) (domain.JobSpec, error) {
	// Claim names and mount paths are server-generated. Clear a value supplied
	// by an API client before validation and storage resolution so it cannot be
	// used to mount an arbitrary PVC.
	spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{}
	spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{}
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
