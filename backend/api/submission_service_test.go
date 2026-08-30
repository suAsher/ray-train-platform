package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
	"ray-train-platform-backend/runtimecatalog"
)

type submissionServiceRepository struct {
	created        *domain.TrainingJob
	createCalls    int
	createErr      error
	identityCalls  int
	artifact       *domain.SourceArtifact
	artifactLookup string
	parent         *domain.TrainingJob
	checkpoints    []domain.TrainingCheckpoint
}

type countingRuntimeImageStore struct {
	stubImageStore
	lookupCalls int
	listCalls   int
	listErr     error
}

func (store *countingRuntimeImageStore) ImageByReference(_ context.Context, tenantID, kind, reference string) (domain.PlatformImage, error) {
	store.lookupCalls++
	for _, image := range store.images {
		if image.Kind == kind && image.Reference == reference && (image.TenantID == "" || image.TenantID == tenantID) {
			return image, nil
		}
	}
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}

func (store *countingRuntimeImageStore) ListImages(ctx context.Context, tenantID, kind string) ([]domain.PlatformImage, error) {
	store.listCalls++
	if store.listErr != nil {
		return nil, store.listErr
	}
	return store.stubImageStore.ListImages(ctx, tenantID, kind)
}

func TestSubmitSnapshotsCatalogRuntimeAndIgnoresForgedRayVersion(t *testing.T) {
	reference := "harbor.example/ray-runtime:production"
	store := &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
		ID: "managed-image", Name: "Ray managed", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}}}}
	repository := &submissionServiceRepository{}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		Images: store, RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
		NewID: func() (string, error) { return "job-runtime-snapshot", nil },
	})
	spec := submissionSpec("  " + reference + "  ")
	spec.TrainingEngine = domain.TrainingEngineRayTrain
	spec.RayVersion = domain.RayVersionCanary

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})

	if err != nil {
		t.Fatalf("submit managed runtime: %v", err)
	}
	if job.Spec.Image != reference || job.Spec.TrainingEngine != domain.TrainingEngineRayTrain || job.Spec.RayVersion != domain.RayVersionProduction {
		t.Fatalf("catalog runtime was not authoritative: %+v", job.Spec)
	}
	if repository.created == nil || repository.created.Spec.RayVersion != domain.RayVersionProduction {
		t.Fatalf("resolved runtime was not persisted: %+v", repository.created)
	}
	if store.lookupCalls != 0 || store.listCalls != 1 {
		t.Fatalf("selected image must use one catalog snapshot, lookup=%d list=%d", store.lookupCalls, store.listCalls)
	}
}

func TestSubmissionRejectsManagedCheckpointOverflowBeforePortalOrNativePersistence(t *testing.T) {
	reference := "harbor.example/ray-runtime:production"
	image := domain.PlatformImage{
		ID: "managed-image", Name: "Ray managed", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	tests := []struct {
		name   string
		origin domain.SubmissionOrigin
		mutate func(*domain.JobSpec)
		field  string
	}{
		{name: "portal checkpoint frequency", origin: domain.SubmissionOriginPortal, mutate: func(spec *domain.JobSpec) { spec.Managed.Checkpoint.EveryEpochs = 100001 }, field: "everyEpochs"},
		{name: "portal latest retention", origin: domain.SubmissionOriginPortal, mutate: func(spec *domain.JobSpec) { spec.Managed.Checkpoint.KeepLatest = 1001 }, field: "keepLatest"},
		{name: "portal best retention", origin: domain.SubmissionOriginPortal, mutate: func(spec *domain.JobSpec) { spec.Managed.Checkpoint.KeepBest = 1001 }, field: "keepBest"},
		{name: "native checkpoint frequency", origin: domain.SubmissionOriginRayCLI, mutate: func(spec *domain.JobSpec) { spec.Managed.Checkpoint.EveryEpochs = 100001 }, field: "everyEpochs"},
		{name: "native latest retention", origin: domain.SubmissionOriginRayCLI, mutate: func(spec *domain.JobSpec) { spec.Managed.Checkpoint.KeepLatest = 1001 }, field: "keepLatest"},
		{name: "native best retention", origin: domain.SubmissionOriginRayCLI, mutate: func(spec *domain.JobSpec) { spec.Managed.Checkpoint.KeepBest = 1001 }, field: "keepBest"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
				RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
			})
			spec := submissionSpec(reference)
			spec.TrainingEngine = domain.TrainingEngineRayTrain
			spec.Managed.MaxFailures = 2
			test.mutate(&spec)

			_, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: test.origin,
			})
			if !errors.Is(err, ErrSubmissionInvalidJobSpec) || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s overflow rejection, got %v", test.field, err)
			}
			if repository.created != nil || repository.identityCalls != 0 {
				t.Fatalf("overflow reached persistence: identity=%d job=%+v", repository.identityCalls, repository.created)
			}
		})
	}
}

func TestSubmissionRejectsUnsafeManagedEntrypointBeforePersistence(t *testing.T) {
	reference := "harbor.example/ray-runtime:production"
	image := domain.PlatformImage{
		ID: "managed-image", Name: "Ray managed", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	tests := []struct {
		name       string
		origin     domain.SubmissionOrigin
		entrypoint domain.Entrypoint
	}{
		{name: "portal direct torchrun", origin: domain.SubmissionOriginPortal, entrypoint: domain.Entrypoint{Command: []string{"torchrun", "train.py"}}},
		{name: "native legacy wrapper operator", origin: domain.SubmissionOriginRayCLI, entrypoint: domain.Entrypoint{Command: []string{"/bin/sh", "-lc", "python train.py && echo unsafe"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
				RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
			})
			spec := submissionSpec(reference)
			spec.TrainingEngine = domain.TrainingEngineRayTrain
			spec.Managed = domain.ManagedTrainingPolicy{
				MaxFailures: 2,
				Checkpoint:  domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
			}
			spec.Entrypoint = test.entrypoint

			job, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: test.origin,
			})
			if job != nil || !errors.Is(err, ErrSubmissionInvalidJobSpec) || !strings.Contains(err.Error(), "managed entrypoint") {
				t.Fatalf("unsafe managed entrypoint returned a job or unclear error: job=%+v err=%v", job, err)
			}
			if repository.created != nil || repository.identityCalls != 0 {
				t.Fatalf("unsafe entrypoint reached persistence: identity=%d job=%+v", repository.identityCalls, repository.created)
			}
		})
	}
}

func TestSubmissionRejectsInvalidRayDataModesBeforeJobAndOutboxPersistence(t *testing.T) {
	reference := "harbor.example/ray-runtime:production"
	image := domain.PlatformImage{
		ID: "managed-image", Name: "Ray managed", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	rayData, err := domain.NewRayDataDatasetConfig(domain.RayDataFormatImages, "images/train")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		mutate  func(*domain.JobSpec)
		wantErr string
	}{
		{name: "ray data missing config", mutate: func(spec *domain.JobSpec) {
			spec.DataMode = domain.DataModeRayData
		}, wantErr: "Ray Data dataset config is required"},
		{name: "mount carries ray data config", mutate: func(spec *domain.JobSpec) {
			spec.DataMode = domain.DataModeMount
			spec.Managed.RayData = rayData
		}, wantErr: "requires ray-data mode"},
		{name: "cache carries ray data config", mutate: func(spec *domain.JobSpec) {
			spec.DataMode = domain.DataModeCache
			spec.Managed.RayData = rayData
			spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "200Gi", Preload: domain.CachePreloadInput}
			spec.Input = domain.DataLocation{Space: domain.DataSpacePublic, RelativePath: "datasets/train"}
		}, wantErr: "requires ray-data mode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
				RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
				LocalCache: LocalCachePolicy{
					Enabled: true, AllowedSizes: []string{"200Gi"}, DefaultSize: "200Gi", MaxSize: "200Gi",
				},
			})
			spec := submissionSpec(reference)
			spec.TrainingEngine = domain.TrainingEngineRayTrain
			spec.Managed = domain.ManagedTrainingPolicy{
				MaxFailures: 2,
				Checkpoint:  domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
			}
			test.mutate(&spec)

			job, submitErr := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: domain.SubmissionOriginPortal,
			})
			if job != nil || !errors.Is(submitErr, ErrSubmissionInvalidJobSpec) || !strings.Contains(submitErr.Error(), test.wantErr) {
				t.Fatalf("invalid Ray Data spec returned job=%+v err=%v", job, submitErr)
			}
			if repository.createCalls != 0 || repository.created != nil || repository.identityCalls != 0 {
				t.Fatalf("invalid Ray Data spec crossed job/outbox persistence boundary: creates=%d identity=%d job=%+v", repository.createCalls, repository.identityCalls, repository.created)
			}
		})
	}
}

func TestRayDataSubmissionRequiresResolvedGovernedInputBeforePersistence(t *testing.T) {
	image, baseSpec := rayDataSubmissionFixture(t)
	for _, test := range []struct {
		name       string
		input      domain.DataLocation
		bindings   []domain.DataMountBinding
		wantDetail string
	}{
		{name: "missing logical input", wantDetail: "resolved governed input mount"},
		{
			name:  "input binding is not ready",
			input: domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/train"},
			bindings: []domain.DataMountBinding{{
				ID: "team", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant,
				SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-team-a", ReadOnly: true, Status: domain.DataMountBindingPending,
			}},
			wantDetail: "data-space mount is not ready",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			tenantRuntimeEnsureCalls := 0
			dataSpaceEnsureCalls := 0
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				Images:            &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
				RuntimePolicy:     runtimecatalog.Policy{ManagedEnabled: true},
				LocalCache:        rayDataLocalCachePolicy(),
				DataSpaces:        &fakeDataSpaceStore{bindings: test.bindings},
				DataSpacesEnabled: true,
				EnsureTenantRuntime: func(context.Context, string, string, string, string) error {
					tenantRuntimeEnsureCalls++
					return nil
				},
				EnsureDataSpaces: func(context.Context, auth.Principal) error {
					dataSpaceEnsureCalls++
					return nil
				},
				NewID: func() (string, error) { return "job-ray-data-input", nil },
			})
			spec := baseSpec
			spec.Input = test.input

			job, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: domain.SubmissionOriginPortal,
			})
			if job != nil || !errors.Is(err, ErrSubmissionDataMountNotReady) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("ray-data input boundary returned job=%+v err=%v", job, err)
			}
			if tenantRuntimeEnsureCalls != 0 || dataSpaceEnsureCalls != 0 || repository.createCalls != 0 || repository.created != nil || repository.identityCalls != 0 {
				t.Fatalf("invalid ray-data input crossed provisioning/persistence: tenantRuntime=%d dataSpaces=%d creates=%d identity=%d job=%+v", tenantRuntimeEnsureCalls, dataSpaceEnsureCalls, repository.createCalls, repository.identityCalls, repository.created)
			}
		})
	}
}

func TestRayDataStagePendingInputDoesNotProvisionOrPersistIdentity(t *testing.T) {
	image, spec := rayDataSubmissionFixture(t)
	files, err := domain.NewRayDataDatasetConfig(domain.RayDataFormatFiles, ".")
	if err != nil {
		t.Fatal(err)
	}
	spec.DataMode = domain.DataModeRayDataStage
	spec.Managed.RayData = files
	spec.Input = domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/train"}

	repository := &submissionServiceRepository{}
	tenantRuntimeEnsureCalls := 0
	dataSpaceEnsureCalls := 0
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
		RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true}, LocalCache: rayDataLocalCachePolicy(),
		DataSpaces: &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
			ID: "team", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant,
			SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-team-a", ReadOnly: true, Status: domain.DataMountBindingPending,
		}}},
		DataSpacesEnabled:   true,
		EnsureTenantRuntime: func(context.Context, string, string, string, string) error { tenantRuntimeEnsureCalls++; return nil },
		EnsureDataSpaces:    func(context.Context, auth.Principal) error { dataSpaceEnsureCalls++; return nil },
		NewID:               func() (string, error) { return "job-ray-data-stage-pending", nil },
	})

	job, submitErr := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if job != nil || !errors.Is(submitErr, ErrSubmissionDataMountNotReady) {
		t.Fatalf("pending stage input returned job=%+v err=%v", job, submitErr)
	}
	if tenantRuntimeEnsureCalls != 0 || dataSpaceEnsureCalls != 0 || repository.identityCalls != 0 || repository.createCalls != 0 {
		t.Fatalf("pending stage input crossed side-effect boundary: runtime=%d spaces=%d identity=%d creates=%d", tenantRuntimeEnsureCalls, dataSpaceEnsureCalls, repository.identityCalls, repository.createCalls)
	}
}

func TestRayDataSubmissionRequiresDualNVMeCapabilityBeforePersistence(t *testing.T) {
	image, spec := rayDataSubmissionFixture(t)
	spec.Input = domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/train"}
	for _, test := range []struct {
		name       string
		policy     LocalCachePolicy
		wantDetail string
	}{
		{name: "runtime cache disabled", wantDetail: "runtime cache capability is disabled"},
		{
			name: "second local cache device unavailable",
			policy: LocalCachePolicy{
				Enabled: true, AllowedSizes: []string{"200Gi"}, DefaultSize: "200Gi", MaxSize: "200Gi",
				MountPaths: []string{"/mnt/cache"},
			},
			wantDetail: "dual-NVMe",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
				RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
				LocalCache:    test.policy,
			})

			job, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: domain.SubmissionOriginPortal,
			})
			if job != nil || !errors.Is(err, ErrSubmissionInvalidJobSpec) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("ray-data cache capability returned job=%+v err=%v", job, err)
			}
			if repository.createCalls != 0 || repository.created != nil || repository.identityCalls != 0 {
				t.Fatalf("unsupported ray-data crossed identity/job/outbox persistence: creates=%d identity=%d job=%+v", repository.createCalls, repository.identityCalls, repository.created)
			}
		})
	}
}

func TestRayDataSubmissionNormalizesRuntimeCacheAndResolvedInput(t *testing.T) {
	image, spec := rayDataSubmissionFixture(t)
	spec.Input = domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/train"}
	repository := &submissionServiceRepository{}
	tenantRuntimeEnsureCalls := 0
	dataSpaceEnsureCalls := 0
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
		RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
		LocalCache:    rayDataLocalCachePolicy(),
		DataSpaces: &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
			ID: "team", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant,
			SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-team-a", ReadOnly: true, Status: domain.DataMountBindingReady,
		}}},
		DataSpacesEnabled: true,
		EnsureTenantRuntime: func(context.Context, string, string, string, string) error {
			tenantRuntimeEnsureCalls++
			if repository.identityCalls != 0 || dataSpaceEnsureCalls != 0 || repository.createCalls != 0 {
				t.Fatalf("ray-data tenant runtime provisioning order changed: identity=%d dataSpaces=%d creates=%d", repository.identityCalls, dataSpaceEnsureCalls, repository.createCalls)
			}
			return nil
		},
		EnsureDataSpaces: func(context.Context, auth.Principal) error {
			dataSpaceEnsureCalls++
			if tenantRuntimeEnsureCalls != 1 || repository.identityCalls != 1 || repository.createCalls != 0 {
				t.Fatalf("ray-data data-space provisioning order changed: tenantRuntime=%d identity=%d creates=%d", tenantRuntimeEnsureCalls, repository.identityCalls, repository.createCalls)
			}
			return nil
		},
		NewID: func() (string, error) { return "job-ray-data-ready", nil },
	})

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil {
		t.Fatalf("submit ready ray-data job: %v", err)
	}
	if job.Spec.Cache.Mode != domain.CacheModeRuntime || job.Spec.Cache.Size != "200Gi" || job.Spec.Cache.Preload != "" {
		t.Fatalf("ray-data cache was not normalized internally: %+v", job.Spec.Cache)
	}
	input := job.Spec.ResolvedDataMounts.Input
	if input == nil || input.MountPath != domain.DataMountInputPath || !input.ReadOnly {
		t.Fatalf("ray-data input did not resolve to the read-only governed path: %+v", input)
	}
	if tenantRuntimeEnsureCalls != 1 || dataSpaceEnsureCalls != 1 || repository.createCalls != 1 || repository.identityCalls != 1 || repository.created == nil || repository.created.Spec.Cache != job.Spec.Cache {
		t.Fatalf("valid ray-data persistence mismatch: tenantRuntime=%d dataSpaces=%d creates=%d identity=%d persisted=%+v", tenantRuntimeEnsureCalls, dataSpaceEnsureCalls, repository.createCalls, repository.identityCalls, repository.created)
	}
}

func rayDataSubmissionFixture(t *testing.T) (domain.PlatformImage, domain.JobSpec) {
	t.Helper()
	reference := "harbor.example/ray-runtime:production"
	image := domain.PlatformImage{
		ID: "managed-image", Name: "Ray managed", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	config, err := domain.NewRayDataDatasetConfig(domain.RayDataFormatImages, "images/train")
	if err != nil {
		t.Fatal(err)
	}
	spec := submissionSpec(reference)
	spec.TrainingEngine = domain.TrainingEngineRayTrain
	spec.DataMode = domain.DataModeRayData
	spec.Managed = domain.ManagedTrainingPolicy{
		MaxFailures: 2, Checkpoint: domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1}, RayData: config,
	}
	return image, spec
}

func rayDataLocalCachePolicy() LocalCachePolicy {
	return LocalCachePolicy{
		Enabled: true, AllowedSizes: []string{"200Gi"}, DefaultSize: "200Gi", MaxSize: "200Gi",
		MountPath: "/mnt/cache", MountPaths: []string{"/mnt/cache", "/mnt/cache2"},
	}
}

func TestSubmitAllowlistFallbackIsLegacyOnly(t *testing.T) {
	reference := "harbor.example/legacy:stable"
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}

	t.Run("omitted engine snapshots legacy runtime", func(t *testing.T) {
		store := &countingRuntimeImageStore{}
		repository := &submissionServiceRepository{}
		service := NewSubmissionService(repository, SubmissionServiceOptions{
			Images: store, ImageAllowlist: []string{reference},
			NewID: func() (string, error) { return "job-legacy-fallback", nil },
		})
		spec := submissionSpec(reference)
		spec.RayVersion = domain.RayVersionCanary

		job, err := service.Submit(context.Background(), SubmissionInput{Principal: principal, Spec: spec, Origin: domain.SubmissionOriginPortal})
		if err != nil {
			t.Fatalf("submit legacy fallback: %v", err)
		}
		if job.Spec.TrainingEngine != domain.TrainingEngineRayDDP || job.Spec.RayVersion != domain.RayVersionLegacy || job.Spec.Image != reference {
			t.Fatalf("unexpected fallback snapshot: %+v", job.Spec)
		}
		if store.lookupCalls != 0 || store.listCalls != 1 {
			t.Fatalf("unexpected catalog calls: lookup=%d list=%d", store.lookupCalls, store.listCalls)
		}
	})

	t.Run("managed requires catalog metadata", func(t *testing.T) {
		store := &countingRuntimeImageStore{}
		repository := &submissionServiceRepository{}
		service := NewSubmissionService(repository, SubmissionServiceOptions{
			Images: store, ImageAllowlist: []string{reference}, RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
		})
		spec := submissionSpec(reference)
		spec.TrainingEngine = domain.TrainingEngineRayTrain

		_, err := service.Submit(context.Background(), SubmissionInput{Principal: principal, Spec: spec, Origin: domain.SubmissionOriginPortal})
		if !errors.Is(err, ErrSubmissionImageNotAllowed) {
			t.Fatalf("expected managed fallback rejection, got %v", err)
		}
		if repository.created != nil {
			t.Fatalf("managed fallback reached persistence: %+v", repository.created)
		}
	})
}

func TestSubmitFailsClosedWhenRuntimeCatalogIsUnavailable(t *testing.T) {
	reference := "harbor.example/legacy:stable"
	store := &countingRuntimeImageStore{
		listErr: errors.New("database unavailable"),
	}
	repository := &submissionServiceRepository{}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		Images: store, ImageAllowlist: []string{reference},
	})

	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      submissionSpec(reference), Origin: domain.SubmissionOriginPortal,
	})

	if !errors.Is(err, ErrSubmissionImageNotAllowed) {
		t.Fatalf("catalog outage must fail closed with the existing public error, got %v", err)
	}
	if repository.created != nil {
		t.Fatalf("catalog outage reached persistence: %+v", repository.created)
	}
}

func TestNewHandlerWiresRuntimePolicyIntoSubmission(t *testing.T) {
	reference := "harbor.example/ray-runtime:production"
	store := &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
		ID: "managed-image", Name: "Ray managed", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}}}}
	handler := NewHandler(&submissionServiceRepository{}, Options{
		Images: store, RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true},
	})
	spec := submissionSpec(reference)
	spec.TrainingEngine = domain.TrainingEngineRayTrain

	job, err := handler.SubmissionService().Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || job.Spec.RayVersion != domain.RayVersionProduction {
		t.Fatalf("handler runtime policy was not wired: job=%+v err=%v", job, err)
	}
}

func TestSubmitScopesCanaryRuntimeToPrincipalTenant(t *testing.T) {
	reference := "harbor.example/ray-runtime:canary"
	image := domain.PlatformImage{
		ID: "canary-image", Name: "Ray canary", Kind: domain.ImageKindTraining, Reference: reference,
		RayVersion: domain.RayVersionCanary, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	tests := []struct {
		name       string
		tenantID   string
		policy     runtimecatalog.Policy
		wantAccept bool
	}{
		{name: "allowlisted tenant", tenantID: "tenant-a", policy: runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}), wantAccept: true},
		{name: "non-allowlisted tenant", tenantID: "tenant-b", policy: runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"})},
		{name: "empty allowlist", tenantID: "tenant-a", policy: runtimecatalog.NewPolicy(true, true, nil, nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				Images:        &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{image}}},
				RuntimePolicy: test.policy,
				NewID:         func() (string, error) { return "job-canary-snapshot", nil },
			})
			spec := submissionSpec(reference)
			spec.TrainingEngine = domain.TrainingEngineRayTrain

			job, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: test.tenantID, Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: domain.SubmissionOriginPortal,
			})
			if test.wantAccept {
				if err != nil || job.Spec.RayVersion != domain.RayVersionCanary {
					t.Fatalf("job=%+v err=%v", job, err)
				}
				return
			}
			if !errors.Is(err, ErrSubmissionImageNotAllowed) || repository.created != nil {
				t.Fatalf("non-canary tenant was not rejected: job=%+v err=%v", repository.created, err)
			}
		})
	}
}

func TestSubmissionServiceDefensivelyCopiesRuntimePolicy(t *testing.T) {
	reference := "harbor.example/ray-runtime:canary"
	tenants := []string{"tenant-a"}
	policy := runtimecatalog.NewPolicy(true, true, nil, tenants)
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "canary-image", Name: "Ray canary", Kind: domain.ImageKindTraining, Reference: reference,
			RayVersion: domain.RayVersionCanary, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy: policy,
		NewID:         func() (string, error) { return "job-policy-copy", nil },
	})
	tenants[0] = "tenant-b"
	spec := submissionSpec(reference)
	spec.TrainingEngine = domain.TrainingEngineRayTrain

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || job.Spec.RayVersion != domain.RayVersionCanary {
		t.Fatalf("caller mutation changed service policy: job=%+v err=%v", job, err)
	}
}

func TestSubmissionNormalizesAndEnforcesRuntimeCachePolicy(t *testing.T) {
	tests := []struct {
		name      string
		policy    LocalCachePolicy
		cache     domain.CacheRequest
		wantMode  domain.CacheMode
		wantSize  string
		wantError string
	}{
		{name: "omitted remains omitted", cache: domain.CacheRequest{}, wantMode: ""},
		{name: "explicit off remains off", cache: domain.CacheRequest{Mode: domain.CacheModeOff}, wantMode: domain.CacheModeOff},
		{name: "runtime rejected while disabled", cache: domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "200Gi"}, wantError: "capability is disabled"},
		{name: "runtime defaults size", policy: LocalCachePolicy{Enabled: true, AllowedSizes: []string{"100Gi", "200Gi", "500Gi"}, DefaultSize: "200Gi", MaxSize: "500Gi"}, cache: domain.CacheRequest{Mode: domain.CacheModeRuntime}, wantMode: domain.CacheModeRuntime, wantSize: "200Gi"},
		{name: "runtime accepts explicit allowed size", policy: LocalCachePolicy{Enabled: true, AllowedSizes: []string{"100Gi", "200Gi", "500Gi"}, DefaultSize: "200Gi", MaxSize: "500Gi"}, cache: domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "100Gi"}, wantMode: domain.CacheModeRuntime, wantSize: "100Gi"},
		{name: "runtime rejects disallowed size", policy: LocalCachePolicy{Enabled: true, AllowedSizes: []string{"100Gi", "200Gi", "500Gi"}, DefaultSize: "200Gi", MaxSize: "500Gi"}, cache: domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "300Gi"}, wantError: "not allowed"},
		{name: "runtime rejects size over max", policy: LocalCachePolicy{Enabled: true, AllowedSizes: []string{"100Gi", "1Ti"}, DefaultSize: "100Gi", MaxSize: "500Gi"}, cache: domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "1Ti"}, wantError: "exceeds maximum"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := NewSubmissionService(repository, SubmissionServiceOptions{
				LocalCache: test.policy,
				NewID:      func() (string, error) { return "job-cache", nil },
			})
			spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("a", 64))
			spec.Cache = test.cache
			job, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: domain.SubmissionOriginPortal,
			})
			if test.wantError != "" {
				if !errors.Is(err, ErrSubmissionInvalidJobSpec) || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected invalid job spec containing %q, got %v", test.wantError, err)
				}
				if repository.created != nil {
					t.Fatalf("invalid cache request was persisted: %#v", repository.created.Spec.Cache)
				}
				return
			}
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if job.Spec.Cache.Mode != test.wantMode || job.Spec.Cache.Size != test.wantSize {
				t.Fatalf("normalized cache=%#v want mode=%q size=%q", job.Spec.Cache, test.wantMode, test.wantSize)
			}
			if repository.created == nil || repository.created.Spec.Cache != job.Spec.Cache {
				t.Fatalf("normalized cache was not persisted: job=%#v persisted=%#v", job.Spec.Cache, repository.created)
			}
		})
	}
}

func TestNormalizeCacheRequestTrimsAutomaticPreload(t *testing.T) {
	cache, err := normalizeCacheRequest(
		domain.CacheRequest{Mode: domain.CacheModeRuntime, Preload: " input "},
		LocalCachePolicy{Enabled: true, AllowedSizes: []string{"1Ti"}, DefaultSize: "1Ti", MaxSize: "1Ti"},
	)
	if err != nil {
		t.Fatalf("normalize automatic preload: %v", err)
	}
	if cache.Preload != domain.CachePreloadInput || cache.Size != "1Ti" {
		t.Fatalf("unexpected normalized cache: %+v", cache)
	}
}

func TestSubmissionServiceCopiesCacheAllowlist(t *testing.T) {
	allowed := []string{"200Gi"}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		LocalCache: LocalCachePolicy{Enabled: true, AllowedSizes: allowed, DefaultSize: "200Gi", MaxSize: "500Gi"},
		NewID:      func() (string, error) { return "job-cache-copy", nil },
	})
	allowed[0] = "500Gi"
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("a", 64))
	spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "200Gi"}
	if _, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	}); err != nil {
		t.Fatalf("constructor retained mutable allowlist: %v", err)
	}
}

func TestSubmissionRestoresTenantRuntimeBeforeCreatingJob(t *testing.T) {
	repository := &submissionServiceRepository{}
	var gotTenant, gotNamespace, gotQueue, gotClusterQueue string
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		ClusterQueue: "platform-gpu",
		EnsureTenantRuntime: func(_ context.Context, tenantID, namespace, queue, clusterQueue string) error {
			gotTenant, gotNamespace, gotQueue, gotClusterQueue = tenantID, namespace, queue, clusterQueue
			return nil
		},
		NewID: func() (string, error) { return "job-restored-tenant", nil },
	})
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      submissionSpec("registry.example/ray@sha256:" + strings.Repeat("a", 64)),
		Origin:    domain.SubmissionOriginPortal,
	})
	if err != nil {
		t.Fatalf("submit restored tenant: %v", err)
	}
	if gotTenant != "team-a" || gotNamespace != "tenant-team-a" || gotQueue != "team-a-gpu" || gotClusterQueue != "platform-gpu" {
		t.Fatalf("unexpected runtime ensure request: tenant=%q namespace=%q queue=%q clusterQueue=%q", gotTenant, gotNamespace, gotQueue, gotClusterQueue)
	}
	if repository.created == nil {
		t.Fatal("job was not persisted after runtime restoration")
	}
}

func TestSubmissionResolvesOnlyVisibleStorageAssets(t *testing.T) {
	asset := domain.StorageAsset{
		ID: "dataset-visible", TenantID: "tenant-a", Name: "dataset", Kind: domain.StorageAssetDataset,
		Provider: domain.StorageProviderTOS, ClaimName: "tos-dataset", RootPrefix: "datasets/tenant-a/", ReadOnly: true, BrowseEnabled: true,
	}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		StorageAssets: &fakeStorageAssetStore{assets: []domain.StorageAsset{asset}},
		NewID:         func() (string, error) { return "job-storage", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("c", 64))
	spec.DatasetStorage = domain.StorageSelection{AssetID: "dataset-foreign", RelativePath: "train"}
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionStorageAssetNotAllowed) {
		t.Fatalf("expected inaccessible storage selection error, got %v", err)
	}
}

func TestSubmissionGeneratesIsolatedOutputDirectory(t *testing.T) {
	asset := domain.StorageAsset{
		ID: "output-visible", TenantID: "tenant-a", Name: "output", Kind: domain.StorageAssetOutput,
		Provider: domain.StorageProviderTOS, ClaimName: "tos-output", RootPrefix: "outputs/tenant-a/user-a/", ReadOnly: false, BrowseEnabled: true,
	}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		StorageAssets: &fakeStorageAssetStore{assets: []domain.StorageAsset{asset}},
		NewID:         func() (string, error) { return "job-storage", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("d", 64))
	spec.OutputStorage = domain.StorageSelection{AssetID: "output-visible"}
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || job.Spec.ResolvedStorage.Output == nil || job.Spec.ResolvedStorage.Output.RelativePath != "runs/job-storage" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
}

func TestSubmissionRejectsLegacyObjectStoreCodeSources(t *testing.T) {
	for _, source := range []domain.CodeSource{
		{Type: "tos", URI: "tos://private-code/user/source.zip"},
		{Type: "artifact", ArtifactID: "legacy-artifact"},
	} {
		t.Run(source.Type, func(t *testing.T) {
			service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
				NewID: func() (string, error) { return "job-legacy-source", nil },
			})
			spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("e", 64))
			spec.Source = source
			_, err := service.Submit(context.Background(), SubmissionInput{
				Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
				Spec:      spec, Origin: domain.SubmissionOriginPortal,
			})
			if !errors.Is(err, ErrSubmissionCodeSourceNotAllowed) {
				t.Fatalf("source=%s err=%v, want code source rejection", source.Type, err)
			}
		})
	}
}

func TestSubmissionAllowsRayCLIWorkspaceArchiveOnlyWithReadyPersonalMount(t *testing.T) {
	artifact := readySubmissionArtifact(t)
	repository := &submissionServiceRepository{artifact: &artifact}
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
		ID: "mine", TenantID: "tenant-a", UserID: "user-01", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-01", Status: domain.DataMountBindingReady,
	}}}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		DataSpaces: store, DataSpacesEnabled: true, NewID: func() (string, error) { return "job-ray-sdk", nil },
	})
	spec := artifactSubmissionSpec()
	spec.Source.Type = "workspace-archive"
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-01", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC},
		Spec:      spec, Origin: domain.SubmissionOriginRayCLI,
	})
	if err != nil || job == nil || job.Spec.Source.ArtifactObjectKey != artifact.ObjectKey || job.Spec.ResolvedDataRoots.Personal == nil {
		t.Fatalf("Ray SDK workspace archive was not resolved safely: job=%#v err=%v", job, err)
	}
}

func TestSubmissionRejectsLogicalDataSpacesUntilPersonalMountIsReady(t *testing.T) {
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
			{ID: "mine", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, Status: domain.DataMountBindingPending},
			{ID: "team", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, Status: domain.DataMountBindingReady, ReadOnly: true},
		}},
		NewID: func() (string, error) { return "job-spaces", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("e", 64))
	spec.Input = domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/v1"}
	spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns}
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionDataMountNotReady) {
		t.Fatalf("expected pending data mount rejection, got %v", err)
	}
}

func TestSubmissionUsesOnlyScopedReadyLogicalDataSpaces(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "tenant-root", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTenantStorageRoot, ClaimName: "data-tenant-a", RootPrefix: "ray-train/", Status: domain.DataMountBindingReady},
		{ID: "mine", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-user-a", ServiceAccountName: "ray-data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/", VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train/tenants/tenant-a/users/user-a"}`, Status: domain.DataMountBindingReady},
		{ID: "team", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-team-a", ServiceAccountName: "ray-data-team-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/shared/", VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train/tenants/tenant-a/shared"}`, Status: domain.DataMountBindingReady, ReadOnly: true},
		{ID: "public", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpacePublic, ClaimName: "data-public", ServiceAccountName: "ray-data-public", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/public/", VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train/public"}`, Status: domain.DataMountBindingReady, ReadOnly: true},
	}}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: store, DataSpacesEnabled: true, NewID: func() (string, error) { return "job-spaces", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("f", 64))
	spec.Input = domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/v1"}
	spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns}
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil {
		t.Fatalf("submit ready logical locations: %v", err)
	}
	if job.Spec.Input.Space != domain.DataSpaceTeamShared || job.Spec.Output.Space != domain.DataSpaceMyRuns {
		t.Fatalf("logical locations changed: %#v", job.Spec)
	}
	if job.Spec.ResolvedDataMounts.Input == nil || job.Spec.ResolvedDataMounts.Input.ClaimName != "data-tenant-a" || job.Spec.ResolvedDataMounts.Input.SubPath != "tenants/tenant-a/shared/datasets/v1" {
		t.Fatalf("input was not resolved to the confined tenant root: %#v", job.Spec.ResolvedDataMounts)
	}
	if job.Spec.ResolvedDataMounts.Output == nil || job.Spec.ResolvedDataMounts.Output.ClaimName != "data-tenant-a" || job.Spec.ResolvedDataMounts.Output.SubPath != "tenants/tenant-a/users/user-a/runs/job-spaces" {
		t.Fatalf("output was not resolved to the confined personal runs directory: %#v", job.Spec.ResolvedDataMounts)
	}
	if job.Spec.ResolvedDataRoots.Personal == nil || job.Spec.ResolvedDataRoots.Personal.ClaimName != "data-tenant-a" || job.Spec.ResolvedDataRoots.Personal.SubPath != "tenants/tenant-a/users/user-a" {
		t.Fatalf("user-visible roots were not mapped to the single tenant claim: %#v", job.Spec.ResolvedDataRoots)
	}
}

func TestSubmissionUsesConfiguredTemporaryPublicDataRoot(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "tenant-root", TenantID: "local", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTenantStorageRoot, ClaimName: "data-tenant-local", RootPrefix: "ray-train/", Status: domain.DataMountBindingReady},
		{ID: "mine", TenantID: "local", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-user-a", RootPrefix: "ray-train/tenants/local/users/guofeng.su/", Status: domain.DataMountBindingReady},
		{ID: "team", TenantID: "local", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-team-local", RootPrefix: "ray-train/tenants/local/shared/", ReadOnly: true, Status: domain.DataMountBindingReady},
		// The persisted dedicated public PVC may still be on the canonical root
		// while the workload uses a temporary root through the tenant-root PVC.
		{ID: "public", TenantID: "local", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpacePublic, ClaimName: "data-public-local", RootPrefix: "ray-train/public/", ReadOnly: true, Status: domain.DataMountBindingReady},
	}}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces:           store,
		DataSpacesEnabled:    true,
		DataSpacesPublicRoot: "ray-train/tenants/local/datasets/public",
		NewID:                func() (string, error) { return "job-public-root", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("a", 64))
	spec.Input = domain.DataLocation{Space: domain.DataSpacePublic, RelativePath: "bevfusion/fz-3dod-v1"}
	spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns}
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "local", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil {
		t.Fatalf("submit with temporary public root: %v", err)
	}
	if job.Spec.ResolvedDataMounts.Input == nil || job.Spec.ResolvedDataMounts.Input.ClaimName != "data-tenant-local" || job.Spec.ResolvedDataMounts.Input.SubPath != "tenants/local/datasets/public/bevfusion/fz-3dod-v1" {
		t.Fatalf("input mount did not use the configured public root: %#v", job.Spec.ResolvedDataMounts.Input)
	}
	if job.Spec.ResolvedDataRoots.Public == nil || job.Spec.ResolvedDataRoots.Public.SubPath != "tenants/local/datasets/public" || !job.Spec.ResolvedDataRoots.Public.ReadOnly {
		t.Fatalf("public user-visible mount did not use the configured root: %#v", job.Spec.ResolvedDataRoots.Public)
	}
}

func TestSubmissionInitializesDataSpacesBeforeResolvingLogicalLocations(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "idc", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCOriginal, ClaimName: "idc-original-a", ReadOnly: true, Status: domain.DataMountBindingReady},
		{ID: "idc-wellspiking", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCWellspiking, ClaimName: "idc-wellspiking-a", ReadOnly: true, Status: domain.DataMountBindingReady},
		{ID: "idc-shared", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCShared, ClaimName: "idc-shared-a", ReadOnly: true, Status: domain.DataMountBindingReady},
	}}
	initialized := 0
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: store, IDCDataSpacesEnabled: true,
		EnsureDataSpaces: func(_ context.Context, principal auth.Principal) error {
			initialized++
			if principal.Subject != "user-a" {
				t.Fatalf("unexpected data-space principal: %#v", principal)
			}
			return nil
		},
		NewID: func() (string, error) { return "job-idc", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("d", 64))
	spec.Input = domain.DataLocation{Space: domain.DataSpaceIDCOriginal}
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || initialized != 1 || job.Spec.ResolvedDataMounts.Input == nil || job.Spec.ResolvedDataMounts.Input.ClaimName != "idc-original-a" {
		t.Fatalf("submission did not initialize and resolve IDC data: job=%#v initialized=%d err=%v", job, initialized, err)
	}
}

func TestSubmissionWaitsForEveryEnabledIDCDataRootToBind(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "idc-original", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCOriginal, ClaimName: "idc-original-a", ReadOnly: true, Status: domain.DataMountBindingReady},
		{ID: "idc-wellspiking", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCWellspiking, ClaimName: "idc-wellspiking-a", ReadOnly: true, Status: domain.DataMountBindingPending},
		{ID: "idc-shared", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCShared, ClaimName: "idc-shared-a", ReadOnly: true, Status: domain.DataMountBindingReady},
	}}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: store, IDCDataSpacesEnabled: true, NewID: func() (string, error) { return "job-idc-pending", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("c", 64))
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionDataMountNotReady) {
		t.Fatalf("submission must wait for every enabled IDC data root: %v", err)
	}
}

func TestSubmissionPlacesOutputBelowSelectedPersonalRunsDirectory(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "mine", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/", VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train/tenants/tenant-a/users/user-a"}`, Status: domain.DataMountBindingReady},
	}}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: store, DataSpacesEnabled: true, NewID: func() (string, error) { return "job-runs-parent", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("a", 64))
	spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "experiments/august"}

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil {
		t.Fatalf("submit with selected result directory: %v", err)
	}
	if got, want := job.Spec.ResolvedDataMounts.Output.SubPath, "runs/experiments/august/job-runs-parent"; got != want {
		t.Fatalf("output subpath=%q want=%q", got, want)
	}
}

func TestSubmissionUsesOnlyOwnerWorkspaceSnapshotAndResolvedPersonalMount(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
		ID: "mine", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal,
		SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-user-a", Driver: domain.FSXCSIDriver,
		RootPrefix: "ray-train/tenants/tenant-a/users/user-a/", VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train/tenants/tenant-a/users/user-a"}`,
		Status: domain.DataMountBindingReady,
	}}}
	snapshots := &fakeWorkspaceSnapshotRepository{items: []domain.WorkspaceSnapshot{{
		ID: "snapshot-a", TenantID: "tenant-a", UserID: "user-a", SourcePath: "project", FileCount: 2,
	}}}
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: store, DataSpacesEnabled: true, WorkspaceSnapshots: snapshots,
		NewID: func() (string, error) { return "job-workspace", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("a", 64))
	spec.Source = domain.CodeSource{Type: "workspace", Snapshot: "snapshot-a"}
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || job.Spec.ResolvedDataRoots.Personal == nil || job.Spec.ResolvedDataRoots.Personal.ClaimName != "data-user-a" {
		t.Fatalf("workspace submission did not retain the owner mount: job=%#v err=%v", job, err)
	}

	foreign := spec
	foreign.Source.Snapshot = "snapshot-other"
	_, err = service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      foreign, Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionWorkspaceSnapshotNotFound) {
		t.Fatalf("foreign workspace snapshot err=%v", err)
	}
}

func TestSubmissionInitializesServerGeneratedOutputDirectoryBeforePersistingJob(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
		ID: "mine", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train/tenants/tenant-a/users/user-a"}`, Status: domain.DataMountBindingReady,
	}}}
	called := ""
	service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{
		DataSpaces: store, DataSpacesEnabled: true, NewID: func() (string, error) { return "job-output", nil },
		EnsureOutputDirectory: func(_ context.Context, _ auth.Principal, mounts domain.ResolvedDataSpaceMounts) error {
			called = mounts.Output.SubPath
			return nil
		},
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("b", 64))
	spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "experiments/a"}
	if _, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginPortal,
	}); err != nil || called != "runs/experiments/a/job-output" {
		t.Fatalf("output initialization=%q err=%v", called, err)
	}
}

func TestSubmissionPersistsOnlyOwnerScopedCompleteResumeCheckpoint(t *testing.T) {
	const parentID = "job-0123456789abcdef01234567"
	const childID = "job-fedcba9876543210fedcba98"
	reference := "registry.example/ray@sha256:" + strings.Repeat("c", 64)
	parent := domain.TrainingJob{
		ID: parentID, TenantID: "tenant-a", UserID: "user-a",
		Spec: domain.JobSpec{TrainingEngine: domain.TrainingEngineRayTrain, Output: domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "parent-run"}},
	}
	checkpoint := domain.TrainingCheckpoint{
		ID: "checkpoint-20", JobID: parentID, TenantID: "tenant-a", UserID: "user-a", Epoch: 2, Step: 20,
		ObjectPath: "/mnt/data/output/.platform/ray-train/" + parentID + "/checkpoints/checkpoint-20",
		Complete:   true, ManifestSHA256: strings.Repeat("a", 64),
	}
	repository := &submissionServiceRepository{parent: &parent, checkpoints: []domain.TrainingCheckpoint{checkpoint}}
	spaces := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
		ID: "mine", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", Status: domain.DataMountBindingReady,
	}}}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "managed", Name: "managed", Kind: domain.ImageKindTraining, Reference: reference,
			RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true}, DataSpaces: spaces, DataSpacesEnabled: true,
		NewID: func() (string, error) { return childID, nil },
	})
	spec := submissionSpec(reference)
	spec.TrainingEngine = domain.TrainingEngineRayTrain
	spec.Managed = domain.ManagedTrainingPolicy{MaxFailures: 2, Checkpoint: domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1}}
	spec.ParentJobID = parentID
	spec.Checkpoint = domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "parent-run/" + parentID + "/.platform/ray-train/" + parentID + "/checkpoints/checkpoint-20"}
	spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "child-run"}

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginRayCLI,
	})
	if err != nil || job.ResumeCheckpointID != checkpoint.ID || repository.created == nil || repository.created.ResumeCheckpointID != checkpoint.ID {
		t.Fatalf("resume provenance was not persisted: job=%+v persisted=%+v err=%v", job, repository.created, err)
	}

	foreign := *repository
	foreign.created = nil
	foreignService := NewSubmissionService(&foreign, SubmissionServiceOptions{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "managed", Name: "managed", Kind: domain.ImageKindTraining, Reference: reference,
			RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}}, RuntimePolicy: runtimecatalog.Policy{ManagedEnabled: true}, NewID: func() (string, error) { return childID, nil },
	})
	_, err = foreignService.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "user-b", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      spec, Origin: domain.SubmissionOriginRayCLI,
	})
	if !errors.Is(err, ErrSubmissionResumeCheckpointNotFound) || foreign.created != nil {
		t.Fatalf("foreign parent checkpoint was accepted: job=%+v err=%v", foreign.created, err)
	}
}

func (repository *submissionServiceRepository) Create(_ context.Context, job *domain.TrainingJob, _ string) error {
	repository.createCalls++
	copy := *job
	repository.created = &copy
	return repository.createErr
}

func (repository *submissionServiceRepository) Get(_ context.Context, tenantID, id string) (*domain.TrainingJob, error) {
	if repository.parent == nil || repository.parent.TenantID != tenantID || repository.parent.ID != id {
		return nil, context.Canceled
	}
	copy := *repository.parent
	return &copy, nil
}

func (repository *submissionServiceRepository) ListUsableCheckpoints(_ context.Context, tenantID, userID, jobID string) ([]domain.TrainingCheckpoint, error) {
	items := make([]domain.TrainingCheckpoint, 0, len(repository.checkpoints))
	for _, checkpoint := range repository.checkpoints {
		if checkpoint.TenantID == tenantID && checkpoint.UserID == userID && checkpoint.JobID == jobID {
			items = append(items, checkpoint)
		}
	}
	return items, nil
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

func TestSubmissionServiceRejectsReadyLegacyArtifactBeforePersistence(t *testing.T) {
	artifact := readySubmissionArtifact(t)
	repository := &submissionServiceRepository{artifact: &artifact}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		NewID: func() (string, error) { return "job-ray-cli", nil },
	})
	principal := auth.Principal{Subject: "user-01", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: principal, Spec: artifactSubmissionSpec(), Origin: domain.SubmissionOriginRayCLI,
	})
	if !errors.Is(err, ErrSubmissionCodeSourceNotAllowed) {
		t.Fatalf("ready legacy artifact must be rejected: %v", err)
	}
	if repository.artifactLookup != "" || repository.created != nil || repository.identityCalls != 0 {
		t.Fatalf("legacy artifact reached persistence or materialization: lookup=%q identity=%d job=%+v", repository.artifactLookup, repository.identityCalls, repository.created)
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
	if !errors.Is(err, ErrSubmissionCodeSourceNotAllowed) {
		t.Fatalf("pending legacy artifact must be rejected before artifact state is read: %v", err)
	}
	if repository.created != nil {
		t.Fatalf("pending artifact reached persistence: %+v", repository.created)
	}
}
