package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
	"ray-train-platform-backend/runtimecatalog"
)

const streamingTestImage = "harbor.example/ray-streaming@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestInternalDatasetPathGuardRejectsEncodingBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		prefix string
	}{
		{name: "default prefix", prefix: domain.DefaultDatasetInternalPrefix},
		{name: "custom prefix", prefix: "private/derived/datasets"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := strings.Trim(test.prefix, "/") + "/private"
			for range 16 {
				candidate = url.PathEscape(candidate)
			}
			service := &SubmissionService{datasetInternalPrefix: test.prefix}
			if !service.referencesInternalDatasetPath(domain.JobSpec{DatasetURI: "tos://bucket/" + candidate}) {
				t.Fatal("sixteen encoding layers must not bypass the internal dataset prefix guard")
			}
		})
	}
}

func streamingDatasetFixtures() (domain.Dataset, domain.DatasetVersion) {
	dataset := domain.Dataset{
		ID: "dataset-labeled-full", Slug: "labeled-full", Name: "Labeled full",
		SourceSpace: domain.DataSpacePublic, SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic,
		SchemaVersion: "s1h-v1",
	}
	version := domain.DatasetVersion{
		ID: "version-20260830", DatasetID: dataset.ID, Version: "2026.08.30", State: domain.DatasetVersionReady,
		ManifestSHA256:    strings.Repeat("b", 64),
		ManifestObjectKey: "ray-train/platform/datasets/" + dataset.ID + "/manifests/version-20260830.parquet",
		SchemaVersion:     "s1h-v1", TrainSamples: 15_228, ValSamples: 1_620,
		SourceObjectCount: 91_216, LogicalBytes: 10 << 40, PackedBytes: 8 << 40,
	}
	return dataset, version
}

func streamingSubmissionSpec() domain.JobSpec {
	spec := submissionSpec(streamingTestImage)
	spec.TrainingEngine = domain.TrainingEngineRayTrain
	spec.DataMode = domain.DataModeStreaming
	spec.DatasetRef = domain.DatasetReference{Dataset: "labeled-full", Version: "latest"}
	spec.CachePolicy = domain.DatasetCachePolicyAuto
	return spec
}

func streamingSubmissionService(repository *submissionServiceRepository, catalog DatasetCatalogStore, versioning, streaming bool, imageRayVersion string, newID func() (string, error)) *SubmissionService {
	if newID == nil {
		newID = func() (string, error) { return "job-streaming-preflight", nil }
	}
	return NewSubmissionService(repository, SubmissionServiceOptions{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "streaming-runtime", Name: "Ray streaming", Kind: domain.ImageKindTraining,
			Reference: streamingTestImage, RayVersion: imageRayVersion,
			SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy: runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}),
		GitAllowlist:  []string{"git.example"}, Datasets: catalog,
		DatasetVersioningEnabled: versioning, RayDataStreamingEnabled: streaming,
		DatasetInternalPrefix: "ray-train/platform/datasets", NewID: newID,
	})
}

func streamingPrincipal() auth.Principal {
	return auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
}

func TestDatasetPreflightResolvesLatestWithoutProvisioningResources(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	repository := &submissionServiceRepository{}
	newIDCalls := 0
	service := streamingSubmissionService(repository, &fakeDatasetCatalog{
		datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
	}, true, true, domain.RayVersionCanary, func() (string, error) {
		newIDCalls++
		return "job-must-not-be-created", nil
	})

	spec := streamingSubmissionSpec()
	spec.DatasetRef.Sites, _ = domain.NewDatasetSites([]string{"cnfzhjyg"})
	result, err := service.Preflight(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginRayCLI,
	})
	if err != nil {
		t.Fatalf("preflight streaming dataset: %v", err)
	}
	if result.Dataset == nil || result.Dataset.DatasetID != dataset.ID || result.Dataset.VersionID != version.ID ||
		result.Dataset.ManifestSHA256 != version.ManifestSHA256 || result.Dataset.SchemaVersion != version.SchemaVersion {
		t.Fatalf("preflight did not return a concrete safe dataset snapshot: %+v", result)
	}
	if result.Image != streamingTestImage || result.TrainingEngine != domain.TrainingEngineRayTrain || result.RayVersion != domain.RayVersionCanary || result.RequestedGPUs != 1 {
		t.Fatalf("preflight runtime/resource summary is wrong: %+v", result)
	}
	if result.Dataset.Sites != spec.DatasetRef.Sites || result.Dataset.SelectionValidation != "pending-manifest-validation" || result.Dataset.TrainSamples != version.TrainSamples {
		t.Fatalf("site selection must remain pinned with full-version counts and explicit pending validation: %+v", result.Dataset)
	}
	if repository.created != nil || repository.identityCalls != 0 || newIDCalls != 0 {
		t.Fatalf("preflight caused submission side effects: created=%+v identity=%d newID=%d", repository.created, repository.identityCalls, newIDCalls)
	}
}

func TestDatasetPreflightAllowsCLIArchiveBeforeItIsUploaded(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	repository := &submissionServiceRepository{}
	service := streamingSubmissionService(repository, &fakeDatasetCatalog{
		datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
	}, true, true, domain.RayVersionCanary, nil)
	spec := streamingSubmissionSpec()
	spec.Source = domain.CodeSource{}

	result, err := service.Preflight(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginRayCLI,
	})
	if err != nil {
		t.Fatalf("preflight before CLI archive upload: %v", err)
	}
	if result.Dataset == nil || result.Dataset.VersionID != version.ID {
		t.Fatalf("preflight did not resolve the immutable version: %+v", result)
	}
	if repository.created != nil || repository.identityCalls != 0 {
		t.Fatalf("preflight created resources: %+v", repository)
	}
}

func TestStreamingBoundedPreflightRequiresDualNVMeBeforeAnySideEffect(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	for _, test := range []struct {
		name       string
		policy     LocalCachePolicy
		wantDetail string
	}{
		{name: "runtime cache disabled", wantDetail: "runtime cache capability is disabled"},
		{
			name: "only one cache mount",
			policy: LocalCachePolicy{
				Enabled: true, AllowedSizes: []string{"100Gi"}, DefaultSize: "100Gi", MaxSize: "100Gi",
				MountPaths: []string{"/mnt/cache"},
			},
			wantDetail: "dual-NVMe",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			newIDCalls := 0
			service := streamingSubmissionService(repository, &fakeDatasetCatalog{
				datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
			}, true, true, domain.RayVersionCanary, func() (string, error) {
				newIDCalls++
				return "job-must-not-be-created", nil
			})
			service.localCache = test.policy
			spec := streamingSubmissionSpec()
			spec.CachePolicy = domain.DatasetCachePolicyBounded

			result, err := service.Preflight(context.Background(), SubmissionInput{
				Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginRayCLI,
			})
			if result != (SubmissionPreflightResult{}) || !errors.Is(err, ErrSubmissionInvalidJobSpec) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("bounded streaming preflight returned result=%+v err=%v", result, err)
			}
			if repository.created != nil || repository.identityCalls != 0 || repository.createCalls != 0 || newIDCalls != 0 {
				t.Fatalf("invalid bounded streaming preflight crossed side-effect boundary: repository=%+v newIDs=%d", repository, newIDCalls)
			}
		})
	}
}

func TestDatasetPreflightRouteReturnsSafeConcreteVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataset, version := streamingDatasetFixtures()
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "streaming-runtime", Name: "Ray streaming", Kind: domain.ImageKindTraining,
			Reference: streamingTestImage, RayVersion: domain.RayVersionCanary,
			SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy:            runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}),
		GitAllowlist:             []string{"git.example"},
		Datasets:                 &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}},
		DatasetVersioningEnabled: true, RayDataStreamingEnabled: true,
	})
	router := gin.New()
	principal := streamingPrincipal()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	payload, err := json.Marshal(submitRequest{Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginRayCLI})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/preflight", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, version.ID) || !strings.Contains(body, version.ManifestSHA256) {
		t.Fatalf("preflight omitted concrete version: %s", body)
	}
	if strings.Contains(body, "manifestObjectKey") || strings.Contains(body, version.ManifestObjectKey) || strings.Contains(body, "ray-train/platform/datasets") {
		t.Fatalf("preflight leaked an internal object key: %s", body)
	}
	if repository.created != nil || repository.identity != 0 {
		t.Fatalf("preflight created resources: repository=%+v", repository)
	}
}

func TestDatasetPreflightRouteRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name      string
		principal *auth.Principal
		body      string
		want      int
	}{
		{name: "authentication required", body: `{}`, want: http.StatusUnauthorized},
		{name: "engineer role required", principal: func() *auth.Principal {
			value := auth.Principal{Subject: "viewer", TenantID: "tenant-a", Roles: []string{"Viewer"}, AuthType: auth.AuthTypeLocal}
			return &value
		}(), body: `{}`, want: http.StatusForbidden},
		{name: "invalid JSON", principal: func() *auth.Principal { value := streamingPrincipal(); return &value }(), body: `{`, want: http.StatusBadRequest},
		{name: "unsupported origin", principal: func() *auth.Principal { value := streamingPrincipal(); return &value }(), body: `{"origin":"api"}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			if test.principal != nil {
				principal := *test.principal
				router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal); c.Next() })
			}
			handler := NewHandler(&fakeJobRepository{}, Options{})
			handler.RegisterTrainingRoutes(router.Group("/api/v1"))
			request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/preflight", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestDatasetPreflightResolvesExplicitDatasetIDAndVersion(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	repository := &submissionServiceRepository{}
	service := streamingSubmissionService(repository, &fakeDatasetCatalog{
		datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
	}, true, true, domain.RayVersionCanary, nil)
	spec := streamingSubmissionSpec()
	spec.DatasetRef = domain.DatasetReference{Dataset: dataset.ID, Version: version.ID}
	result, err := service.Preflight(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || result.Dataset == nil || result.Dataset.VersionID != version.ID {
		t.Fatalf("explicit dataset preflight failed: result=%+v err=%v", result, err)
	}
}

func TestDatasetReferenceParserAndAmbiguousSlugFailClosed(t *testing.T) {
	if _, _, err := parseDatasetReference(domain.DatasetReference{}); err == nil {
		t.Fatal("empty dataset reference was accepted")
	}
	if _, _, err := parseDatasetReference(domain.DatasetReference{Dataset: "labeled-full", Version: "bad/version"}); err == nil {
		t.Fatal("unsafe dataset version selector was accepted")
	}
	dataset, version := streamingDatasetFixtures()
	teamDataset := dataset
	teamDataset.ID = "dataset-team-copy"
	teamDataset.SourceSpace = domain.DataSpaceTeamShared
	teamDataset.OwnerTenantID = "tenant-a"
	teamDataset.Visibility = domain.DatasetVisibilityTeam
	teamVersion := version
	teamVersion.DatasetID = teamDataset.ID
	teamVersion.ID = "version-team-copy"
	teamVersion.ManifestObjectKey = "ray-train/platform/datasets/" + teamDataset.ID + "/manifests/" + teamVersion.ID + ".parquet"
	repository := &submissionServiceRepository{}
	service := streamingSubmissionService(repository, &fakeDatasetCatalog{
		datasets: []domain.Dataset{dataset, teamDataset}, versions: []domain.DatasetVersion{version, teamVersion},
	}, true, true, domain.RayVersionCanary, nil)
	_, err := service.Preflight(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionDatasetNotFound) {
		t.Fatalf("ambiguous public/team slug did not fail closed: %v", err)
	}
}

func TestSubmissionPinsReadyDatasetVersionAndDoesNotFollowLaterLatest(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	catalog := &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}}
	repository := &submissionServiceRepository{}
	service := streamingSubmissionService(repository, catalog, true, true, domain.RayVersionCanary, nil)

	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginRayCLI,
	})
	if err != nil {
		t.Fatalf("submit streaming dataset: %v", err)
	}
	if job.DatasetProvenance.DatasetID != dataset.ID || job.DatasetProvenance.DatasetVersionID != version.ID ||
		job.DatasetProvenance.ManifestSHA256 != version.ManifestSHA256 || job.DatasetProvenance.DataMode != domain.DataModeStreaming ||
		job.DatasetProvenance.CachePolicy != domain.DatasetCachePolicyAuto {
		t.Fatalf("job did not persist immutable dataset provenance: %+v", job.DatasetProvenance)
	}
	if job.Spec.DatasetRef != (domain.DatasetReference{Dataset: dataset.ID, Version: version.ID}) {
		t.Fatalf("latest selector remained mutable in persisted spec: %+v", job.Spec.DatasetRef)
	}

	newVersion := version
	newVersion.ID = "version-20260831"
	newVersion.Version = "2026.08.31"
	newVersion.ManifestSHA256 = strings.Repeat("c", 64)
	newVersion.ManifestObjectKey = "ray-train/platform/datasets/" + dataset.ID + "/manifests/" + newVersion.ID + ".parquet"
	catalog.versions = append(catalog.versions, newVersion)
	if repository.created == nil || repository.created.DatasetProvenance.DatasetVersionID != version.ID || repository.created.DatasetProvenance.ManifestSHA256 != version.ManifestSHA256 {
		t.Fatalf("already-created job followed a later READY version: %+v", repository.created)
	}
}

func TestSubmissionRejectsInvalidDatasetSnapshotsBeforeAnySideEffect(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	tests := []struct {
		name       string
		datasets   []domain.Dataset
		versions   []domain.DatasetVersion
		resolveErr error
		rayVersion string
		mutateSpec func(*domain.JobSpec)
		want       error
	}{
		{name: "feature disabled", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetFeatureDisabled},
		{name: "not ready", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, resolveErr: repositories.ErrDatasetVersionNotReady, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetVersionNotReady},
		{name: "foreign dataset", datasets: []domain.Dataset{{
			ID: "foreign-data", Slug: "labeled-full", Name: "Foreign", SourceSpace: domain.DataSpaceTeamShared,
			SourceRelativePath: "labeled", OwnerTenantID: "tenant-b", Visibility: domain.DatasetVisibilityTeam, SchemaVersion: "s1h-v1",
		}}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetNotFound},
		{name: "schema mismatch", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{func() domain.DatasetVersion {
			invalid := version
			invalid.SchemaVersion = "s1h-v2"
			return invalid
		}()}, rayVersion: domain.RayVersionCanary, mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetIncompatible},
		{name: "invalid catalog metadata", datasets: []domain.Dataset{func() domain.Dataset {
			invalid := dataset
			invalid.Name = ""
			return invalid
		}()}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetIncompatible},
		{name: "manifest missing", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{func() domain.DatasetVersion {
			invalid := version
			invalid.ManifestSHA256 = ""
			invalid.ManifestObjectKey = ""
			return invalid
		}()}, rayVersion: domain.RayVersionCanary, mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetManifestInvalid},
		{name: "runtime incompatible", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionProduction,
			mutateSpec: func(*domain.JobSpec) {}, want: ErrSubmissionDatasetIncompatible},
		{name: "internal URI supplied", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(spec *domain.JobSpec) { spec.DatasetURI = "tos://bucket/ray-train/platform/datasets/private" }, want: ErrSubmissionDatasetInternalPath},
		{name: "encoded internal URI supplied", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(spec *domain.JobSpec) { spec.DatasetURI = "tos://bucket/ray-train%2Fplatform%2Fdatasets/private" }, want: ErrSubmissionDatasetInternalPath},
		{name: "multiply encoded internal URI supplied", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(spec *domain.JobSpec) {
				spec.DatasetURI = "tos://bucket/ray-train%25252Fplatform%25252Fdatasets/private"
			}, want: ErrSubmissionDatasetInternalPath},
		{name: "exactly sixteen encoded layers of internal URI supplied", datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}, rayVersion: domain.RayVersionCanary,
			mutateSpec: func(spec *domain.JobSpec) {
				candidate := "ray-train/platform/datasets/private"
				for range 16 {
					candidate = url.PathEscape(candidate)
				}
				spec.DatasetURI = "tos://bucket/" + candidate
			}, want: ErrSubmissionDatasetInternalPath},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			catalog := &fakeDatasetCatalog{datasets: test.datasets, versions: test.versions, resolveErr: test.resolveErr}
			versioning, streaming := true, true
			if test.name == "feature disabled" {
				versioning, streaming = false, false
			}
			newIDCalls := 0
			service := streamingSubmissionService(repository, catalog, versioning, streaming, test.rayVersion, func() (string, error) {
				newIDCalls++
				return "job-should-not-exist", nil
			})
			spec := streamingSubmissionSpec()
			test.mutateSpec(&spec)
			_, err := service.Submit(context.Background(), SubmissionInput{
				Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginRayCLI,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
			if repository.created != nil || repository.identityCalls != 0 || newIDCalls != 0 {
				t.Fatalf("invalid dataset reached a side effect: created=%+v identity=%d newID=%d", repository.created, repository.identityCalls, newIDCalls)
			}
		})
	}
}

func TestSubmissionKeepsLegacyDataSpaceSubmissionCompatible(t *testing.T) {
	repository := &submissionServiceRepository{}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		ImageAllowlist: []string{"registry.example/ray"}, DatasetVersioningEnabled: true, RayDataStreamingEnabled: true,
		NewID: func() (string, error) { return "job-legacy-compatible", nil },
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("d", 64))
	spec.DataMode = domain.DataModeMount
	job, err := service.Submit(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginPortal,
	})
	if err != nil {
		t.Fatalf("legacy submission regressed: %v", err)
	}
	if !job.DatasetProvenance.IsZero() || !job.Spec.DatasetRef.IsZero() {
		t.Fatalf("legacy job unexpectedly acquired dataset provenance: %+v", job)
	}
}

func TestSubmissionRejectsEncodedInternalDatasetURIInLegacyMode(t *testing.T) {
	repository := &submissionServiceRepository{}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		ImageAllowlist: []string{"registry.example/ray"}, DatasetVersioningEnabled: true, RayDataStreamingEnabled: true,
		DatasetInternalPrefix: "ray-train/platform/datasets",
	})
	spec := submissionSpec("registry.example/ray@sha256:" + strings.Repeat("e", 64))
	spec.DatasetURI = "tos://bucket/ray-train%2Fplatform%2Fdatasets/private"
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: spec, Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionDatasetInternalPath) {
		t.Fatalf("encoded internal dataset URI was not rejected: %v", err)
	}
	if repository.created != nil || repository.identityCalls != 0 {
		t.Fatalf("encoded internal dataset URI reached side effects: %+v", repository)
	}
}

func TestDatasetPreflightSupportsConfiguredInternalPrefix(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	version.ManifestObjectKey = "private/platform/datasets/" + dataset.ID + "/manifests/" + version.ID + ".parquet"
	repository := &submissionServiceRepository{}
	service := NewSubmissionService(repository, SubmissionServiceOptions{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "streaming-runtime", Name: "Ray streaming", Kind: domain.ImageKindTraining,
			Reference: streamingTestImage, RayVersion: domain.RayVersionCanary,
			SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy:            runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}),
		Datasets:                 &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}},
		DatasetVersioningEnabled: true, RayDataStreamingEnabled: true,
		DatasetInternalPrefix: "private/platform/datasets",
	})

	result, err := service.Preflight(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginPortal,
	})
	if err != nil || result.Dataset == nil || result.Dataset.VersionID != version.ID {
		t.Fatalf("custom-prefix preflight failed: result=%+v err=%v", result, err)
	}
}

func TestDatasetPreflightReportsCatalogOutagesWithoutSideEffects(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	tests := []struct {
		name    string
		catalog *fakeDatasetCatalog
	}{
		{name: "list outage", catalog: &fakeDatasetCatalog{listErr: errors.New("database unavailable")}},
		{name: "resolve outage", catalog: &fakeDatasetCatalog{
			datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
			resolveErr: errors.New("database unavailable"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &submissionServiceRepository{}
			service := streamingSubmissionService(repository, test.catalog, true, true, domain.RayVersionCanary, nil)
			_, err := service.Preflight(context.Background(), SubmissionInput{
				Principal: streamingPrincipal(), Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginPortal,
			})
			if !errors.Is(err, ErrSubmissionDatasetCatalogUnavailable) {
				t.Fatalf("catalog outage error=%v", err)
			}
			if repository.created != nil || repository.identityCalls != 0 {
				t.Fatalf("catalog outage reached side effects: %+v", repository)
			}
		})
	}
}

func TestDatasetPreflightRouteMapsCatalogOutageToServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataset, version := streamingDatasetFixtures()
	handler := NewHandler(&fakeJobRepository{}, Options{
		Images: &countingRuntimeImageStore{stubImageStore: stubImageStore{images: []domain.PlatformImage{{
			ID: "streaming-runtime", Name: "Ray streaming", Kind: domain.ImageKindTraining,
			Reference: streamingTestImage, RayVersion: domain.RayVersionCanary,
			SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
		}}}},
		RuntimePolicy: runtimecatalog.NewPolicy(true, true, nil, []string{"tenant-a"}),
		Datasets: &fakeDatasetCatalog{
			datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
			resolveErr: errors.New("database unavailable"),
		},
		DatasetVersioningEnabled: true, RayDataStreamingEnabled: true,
	})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", streamingPrincipal()); c.Next() })
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	payload, _ := json.Marshal(submitRequest{Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginPortal})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/preflight", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "DATASET_CATALOG_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSubmissionMapsDatasetSnapshotTOCTOUToRetryableConflict(t *testing.T) {
	dataset, version := streamingDatasetFixtures()
	repository := &submissionServiceRepository{createErr: repositories.ErrDatasetSnapshotConflict}
	service := streamingSubmissionService(repository, &fakeDatasetCatalog{
		datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version},
	}, true, true, domain.RayVersionCanary, nil)

	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: streamingPrincipal(), Spec: streamingSubmissionSpec(), Origin: domain.SubmissionOriginPortal,
	})
	if !errors.Is(err, ErrSubmissionDatasetVersionNotReady) {
		t.Fatalf("snapshot race error=%v", err)
	}
}
