package datasetpublisher

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestManagerCreatesImmutablePublicationRequestsWithOwningScope(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 34, 56, 0, time.UTC)
	tests := []struct {
		name        string
		dataset     domain.Dataset
		wantTenant  string
		wantAdmin   bool
		distributed bool
	}{
		{
			name: "public",
			dataset: domain.Dataset{ID: "public-data", Slug: "labeled", Name: "Labeled", SourceSpace: domain.DataSpacePublic,
				SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "parquet-v1"},
			wantAdmin: true,
		},
		{
			name: "team distributed",
			dataset: domain.Dataset{ID: "team-data", Slug: "team-labeled", Name: "Team labeled", SourceSpace: domain.DataSpaceTeamShared,
				SourceRelativePath: "datasets/labeled", OwnerTenantID: "tenant-a", Visibility: domain.DatasetVisibilityTeam, SchemaVersion: "parquet-v1"},
			wantTenant: "tenant-a", distributed: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &managerRepository{}
			ids := []string{"version-0123456789abcdef", "publication-0123456789abcdef"}
			manager := mustPublicationManager(t, repository, &recordingPublicationController{}, ManagerOptions{
				PublicRoot: "ray-train/public", SourceIndexName: ".raytrain/trusted-index-v2.pkl",
				DistributedEnabled: test.distributed,
				Now:                func() time.Time { return now },
				NewID: func(_ string) (string, error) {
					value := ids[0]
					ids = ids[1:]
					return value, nil
				},
			})

			run, err := manager.RequestDatasetPublication(context.Background(), test.dataset, "requesting-user")
			if err != nil {
				t.Fatalf("request publication: %v", err)
			}
			if repository.createTenant != test.wantTenant || repository.createSuperAdmin != test.wantAdmin {
				t.Fatalf("scope tenant=%q admin=%t", repository.createTenant, repository.createSuperAdmin)
			}
			if repository.createdVersion.ID != "version-0123456789abcdef" || repository.createdVersion.DatasetID != test.dataset.ID || repository.createdVersion.State != domain.DatasetVersionDiscovering || repository.createdVersion.SchemaVersion != test.dataset.SchemaVersion {
				t.Fatalf("unexpected version: %+v", repository.createdVersion)
			}
			if repository.createdVersion.Version != "20260830T123456Z+0123456789ab" {
				t.Fatalf("unexpected display version: %q", repository.createdVersion.Version)
			}
			if run.ID != "publication-0123456789abcdef" || run.DatasetVersionID != repository.createdVersion.ID || run.State != domain.DatasetVersionDiscovering {
				t.Fatalf("unexpected run: %+v", run)
			}
			wantMode := domain.DatasetPublicationExecutionLegacy
			if test.distributed {
				wantMode = domain.DatasetPublicationExecutionDistributed
			}
			if run.ExecutionMode != wantMode || repository.createdRun.ExecutionMode != wantMode {
				t.Fatalf("run execution mode=%q persisted=%q want=%q", run.ExecutionMode, repository.createdRun.ExecutionMode, wantMode)
			}
		})
	}
}

func TestManagerReconcilesPublicAndTeamSourcesWithoutExposingObjectURIs(t *testing.T) {
	public := domain.Dataset{ID: "public-data", Slug: "labeled", Name: "Labeled", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "parquet-v1"}
	team := domain.Dataset{ID: "team-data", Slug: "team-labeled", Name: "Team", SourceSpace: domain.DataSpaceTeamShared,
		SourceRelativePath: "datasets/labeled", OwnerTenantID: "tenant-a", Visibility: domain.DatasetVisibilityTeam, SchemaVersion: "parquet-v1"}
	work := []domain.DatasetPublicationWork{
		publicationWork(public, "version-public", "publication-public", "20260830.1"),
		publicationWork(team, "version-team", "publication-team", "20260830.2"),
	}
	repository := &managerRepository{work: work}
	controller := &recordingPublicationController{}
	manager := mustPublicationManager(t, repository, controller, ManagerOptions{
		PublicRoot: "ray-train/public/", SourceIndexName: ".raytrain/trusted-index-v2.pkl", BatchLimit: 16,
	})

	if err := manager.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if repository.listLimit != 16 || len(controller.requests) != 2 {
		t.Fatalf("list limit=%d requests=%d", repository.listLimit, len(controller.requests))
	}
	want := []ReconcileRequest{
		{TenantID: "", SuperAdmin: true, RunID: "publication-public", DatasetID: "public-data", DatasetVersionID: "version-public", Version: "20260830.1", SchemaVersion: "parquet-v1", SourceRoot: "ray-train/public/labeled", SourceIndex: ".raytrain/trusted-index-v2.pkl"},
		{TenantID: "tenant-a", SuperAdmin: false, RunID: "publication-team", DatasetID: "team-data", DatasetVersionID: "version-team", Version: "20260830.2", SchemaVersion: "parquet-v1", SourceRoot: "ray-train/tenants/tenant-a/shared/datasets/labeled", SourceIndex: ".raytrain/trusted-index-v2.pkl"},
	}
	if !reflect.DeepEqual(controller.requests, want) {
		t.Fatalf("requests = %#v, want %#v", controller.requests, want)
	}
}

func TestManagerReconcilesNewDiscoveringPublication(t *testing.T) {
	dataset := domain.Dataset{ID: "public-data", Slug: "labeled", Name: "Labeled", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "parquet-v1"}
	work := publicationWork(dataset, "version-public", "publication-public", "20260830.1")
	work.Version.State = domain.DatasetVersionDiscovering
	work.Run.State = domain.DatasetVersionDiscovering
	repository := &managerRepository{work: []domain.DatasetPublicationWork{work}}
	controller := &recordingPublicationController{}
	manager := mustPublicationManager(t, repository, controller, ManagerOptions{
		PublicRoot: "ray-train/public", SourceIndexName: ".raytrain/trusted-index-v2.pkl",
	})

	if err := manager.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile discovering publication: %v", err)
	}
	if len(controller.requests) != 1 || controller.requests[0].RunID != work.Run.ID {
		t.Fatalf("requests=%+v", controller.requests)
	}
}

func TestManagerContinuesOtherPublicationsAndReportsSanitizedFailure(t *testing.T) {
	dataset := domain.Dataset{ID: "public-data", Slug: "labeled", Name: "Labeled", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "parquet-v1"}
	repository := &managerRepository{work: []domain.DatasetPublicationWork{
		publicationWork(dataset, "version-a", "publication-a", "20260830.1"),
		publicationWork(dataset, "version-b", "publication-b", "20260830.2"),
	}}
	controller := &recordingPublicationController{failRunID: "publication-a", failure: errors.New("secret endpoint response")}
	manager := mustPublicationManager(t, repository, controller, ManagerOptions{PublicRoot: "ray-train/public", SourceIndexName: ".raytrain/trusted-index-v2.pkl"})

	err := manager.ReconcileOnce(context.Background())
	if !errors.Is(err, ErrPublicationManagerUnavailable) || len(controller.requests) != 2 {
		t.Fatalf("err=%v requests=%d", err, len(controller.requests))
	}
	if err.Error() != ErrPublicationManagerUnavailable.Error() {
		t.Fatalf("dependency details leaked: %q", err)
	}
	diagnostic := ReconcileDiagnostic(err)
	if diagnostic != "controller reconcile failed for publication-a" {
		t.Fatalf("diagnostic=%q", diagnostic)
	}
	if strings.Contains(diagnostic, "secret endpoint response") {
		t.Fatalf("dependency details leaked through diagnostic: %q", diagnostic)
	}
}

func TestManagerReportsKnownControllerFailureClassWithoutDependencyDetails(t *testing.T) {
	dataset := domain.Dataset{ID: "public-data", Slug: "labeled", Name: "Labeled", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "parquet-v1"}
	repository := &managerRepository{work: []domain.DatasetPublicationWork{publicationWork(dataset, "version-a", "publication-a", "20260830.1")}}
	controller := &recordingPublicationController{failRunID: "publication-a", failure: fmt.Errorf("wrapped secret: %w", ErrPublicationJobUnavailable)}
	manager := mustPublicationManager(t, repository, controller, ManagerOptions{PublicRoot: "ray-train/public", SourceIndexName: ".raytrain/trusted-index-v2.pkl"})

	err := manager.ReconcileOnce(context.Background())
	want := "controller reconcile: publication job unavailable failed for publication-a"
	if got := ReconcileDiagnostic(err); got != want || strings.Contains(got, "wrapped secret") {
		t.Fatalf("diagnostic=%q want=%q", got, want)
	}
}

func TestManagerReportsSafeListAndRequestDiagnostics(t *testing.T) {
	dataset := domain.Dataset{ID: "public-data", Slug: "labeled", Name: "Labeled", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "parquet-v1"}
	tests := []struct {
		name       string
		repository *managerRepository
		want       string
	}{
		{name: "list", repository: &managerRepository{listFailure: errors.New("database DSN with secret")}, want: "list active publications failed"},
		{name: "request", repository: &managerRepository{work: []domain.DatasetPublicationWork{publicationWork(dataset, "version-a", "publication-a", "bad/version")}}, want: "build reconcile request failed for publication-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := mustPublicationManager(t, test.repository, &recordingPublicationController{}, ManagerOptions{PublicRoot: "ray-train/public", SourceIndexName: ".raytrain/trusted-index-v2.pkl"})
			err := manager.ReconcileOnce(context.Background())
			if !errors.Is(err, ErrPublicationManagerUnavailable) || err.Error() != ErrPublicationManagerUnavailable.Error() {
				t.Fatalf("error=%v", err)
			}
			if got := ReconcileDiagnostic(err); got != test.want || strings.Contains(got, "secret") {
				t.Fatalf("diagnostic=%q want=%q", got, test.want)
			}
		})
	}
}

func TestManagerGCDryRunDelegatesToAuthoritativeRepository(t *testing.T) {
	version := domain.DatasetVersion{ID: "version-old", DatasetID: "public-data", Version: "20260801.1", State: domain.DatasetVersionDeprecated,
		ManifestSHA256: testPublicationDigestA, ManifestObjectKey: "ray-train/platform/datasets/public-data/manifests/version-old.parquet", SchemaVersion: "parquet-v1"}
	repository := &managerRepository{gc: []domain.DatasetVersion{version}}
	manager := mustPublicationManager(t, repository, &recordingPublicationController{}, ManagerOptions{PublicRoot: "ray-train/public", SourceIndexName: ".raytrain/trusted-index-v2.pkl"})

	got, err := manager.DryRunDatasetVersionGC(context.Background())
	if err != nil || !reflect.DeepEqual(got, []domain.DatasetVersion{version}) {
		t.Fatalf("gc=%+v err=%v", got, err)
	}
	got[0].ID = "mutated"
	if repository.gc[0].ID != "version-old" {
		t.Fatal("manager returned repository-owned GC storage")
	}
}

func mustPublicationManager(t *testing.T, repository PublicationManagerRepository, controller PublicationReconciler, options ManagerOptions) *Manager {
	t.Helper()
	manager, err := NewManager(repository, controller, options)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func publicationWork(dataset domain.Dataset, versionID, runID, version string) domain.DatasetPublicationWork {
	return domain.DatasetPublicationWork{
		Dataset: dataset,
		Version: domain.DatasetVersion{ID: versionID, DatasetID: dataset.ID, Version: version, State: domain.DatasetVersionStabilizing, SchemaVersion: dataset.SchemaVersion},
		Run:     domain.DatasetPublicationRun{ID: runID, DatasetID: dataset.ID, DatasetVersionID: versionID, ExecutionMode: domain.DatasetPublicationExecutionLegacy, State: domain.DatasetVersionStabilizing},
	}
}

type managerRepository struct {
	createTenant     string
	createSuperAdmin bool
	createdVersion   domain.DatasetVersion
	createdRun       domain.DatasetPublicationRun
	work             []domain.DatasetPublicationWork
	listLimit        int
	gc               []domain.DatasetVersion
	listFailure      error
}

func (repository *managerRepository) CreateDatasetPublicationRequest(_ context.Context, tenantID string, superAdmin bool, version domain.DatasetVersion, run domain.DatasetPublicationRun) (domain.DatasetPublicationRun, error) {
	repository.createTenant, repository.createSuperAdmin = tenantID, superAdmin
	repository.createdVersion, repository.createdRun = version, run
	return run, nil
}

func (repository *managerRepository) ListActiveDatasetPublications(_ context.Context, limit int) ([]domain.DatasetPublicationWork, error) {
	repository.listLimit = limit
	if repository.listFailure != nil {
		return nil, repository.listFailure
	}
	return append([]domain.DatasetPublicationWork(nil), repository.work...), nil
}

func (repository *managerRepository) ListDatasetVersionGCCandidates(context.Context) ([]domain.DatasetVersion, error) {
	return append([]domain.DatasetVersion(nil), repository.gc...), nil
}

type recordingPublicationController struct {
	requests  []ReconcileRequest
	failRunID string
	failure   error
}

func (controller *recordingPublicationController) Reconcile(_ context.Context, request ReconcileRequest) (domain.DatasetPublicationRun, error) {
	controller.requests = append(controller.requests, request)
	if request.RunID == controller.failRunID {
		return domain.DatasetPublicationRun{}, controller.failure
	}
	return domain.DatasetPublicationRun{ID: request.RunID, DatasetID: request.DatasetID, DatasetVersionID: request.DatasetVersionID, State: domain.DatasetVersionStabilizing}, nil
}
