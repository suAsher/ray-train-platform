package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

const testDatasetManifestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeDatasetManifestCatalog struct {
	version  domain.DatasetVersion
	bindings []domain.DataMountBinding
	getErr   error
	listErr  error
	tenantID string
}

func (catalog *fakeDatasetManifestCatalog) GetDatasetVersion(_ context.Context, tenantID string, superAdmin bool, datasetID, versionID string) (domain.DatasetVersion, error) {
	catalog.tenantID = tenantID
	if superAdmin {
		return domain.DatasetVersion{}, errors.New("resolver must preserve tenant visibility")
	}
	if catalog.getErr != nil {
		return domain.DatasetVersion{}, catalog.getErr
	}
	if catalog.version.DatasetID != datasetID || catalog.version.ID != versionID {
		return domain.DatasetVersion{}, errors.New("not found")
	}
	return catalog.version, nil
}

func (catalog *fakeDatasetManifestCatalog) ListDataBindings(_ context.Context, tenantID, userID string) ([]domain.DataMountBinding, error) {
	if tenantID == "" || userID != "" {
		return nil, errors.New("resolver requested the wrong binding scope")
	}
	if catalog.listErr != nil {
		return nil, catalog.listErr
	}
	return append([]domain.DataMountBinding(nil), catalog.bindings...), nil
}

func readyDatasetVersion() domain.DatasetVersion {
	return domain.DatasetVersion{
		ID: "version-20260830", DatasetID: "dataset-labeled-full", Version: "20260830.1",
		State: domain.DatasetVersionReady, ManifestSHA256: testDatasetManifestDigest,
		ManifestObjectKey: "ray-train/platform/datasets/dataset-labeled-full/manifests/version-20260830.parquet",
		SchemaVersion:     "s1h-lidar-parquet-v1", TrainSamples: 15228,
	}
}

func readyTenantRootBinding() domain.DataMountBinding {
	return domain.DataMountBinding{
		ID: "tos-root-abc", TenantID: "local", Scope: domain.DataMountScopeTenant,
		SpaceID: domain.DataSpaceTenantStorageRoot, ClaimName: "data-tenant-local",
		Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"test","path":"/ray-train"}`,
		Status:               domain.DataMountBindingReady,
	}
}

func datasetResolutionRequest() k8s.DatasetManifestResolutionRequest {
	return k8s.DatasetManifestResolutionRequest{
		TenantID: "local", DatasetID: "dataset-labeled-full",
		DatasetVersionID: "version-20260830", ManifestSHA256: testDatasetManifestDigest,
	}
}

func TestPrivateDatasetManifestResolverReturnsOnlySelectedDatasetRoot(t *testing.T) {
	catalog := &fakeDatasetManifestCatalog{
		version: readyDatasetVersion(), bindings: []domain.DataMountBinding{readyTenantRootBinding()},
	}
	resolver, err := newPrivateDatasetManifestResolver(catalog, "ray-train/platform/datasets")
	if err != nil {
		t.Fatal(err)
	}

	mount, err := resolver.ResolveDatasetManifestMount(context.Background(), datasetResolutionRequest())
	if err != nil {
		t.Fatalf("resolve dataset mount: %v", err)
	}
	want := k8s.DatasetManifestMount{
		DatasetID: "dataset-labeled-full", DatasetVersionID: "version-20260830",
		ManifestSHA256: testDatasetManifestDigest, TrainSamples: 15228,
		ClaimName: "data-tenant-local", DatasetRootSubPath: "platform/datasets/dataset-labeled-full",
	}
	if mount != want {
		t.Fatalf("mount=%+v, want %+v", mount, want)
	}
	if catalog.tenantID != "local" {
		t.Fatalf("catalog visibility tenant=%q", catalog.tenantID)
	}
}

func TestPrivateDatasetManifestResolverFailsClosedOnStaleOrUnsafeCatalogState(t *testing.T) {
	baseVersion := readyDatasetVersion()
	baseBinding := readyTenantRootBinding()
	tests := []struct {
		name      string
		version   domain.DatasetVersion
		binding   domain.DataMountBinding
		request   k8s.DatasetManifestResolutionRequest
		getErr    error
		listErr   error
		wantError string
	}{
		{name: "catalog unavailable", version: baseVersion, binding: baseBinding, request: datasetResolutionRequest(), getErr: errors.New("database host secret"), wantError: "dataset version is unavailable"},
		{name: "wrong digest", version: baseVersion, binding: baseBinding, request: func() k8s.DatasetManifestResolutionRequest {
			value := datasetResolutionRequest()
			value.ManifestSHA256 = strings.Repeat("b", 64)
			return value
		}(), wantError: "provenance does not match"},
		{name: "not ready", version: func() domain.DatasetVersion {
			value := baseVersion
			value.State = domain.DatasetVersionPacking
			value.ManifestSHA256 = ""
			value.ManifestObjectKey = ""
			return value
		}(), binding: baseBinding, request: datasetResolutionRequest(), wantError: "not ready"},
		{name: "wrong internal key", version: func() domain.DatasetVersion {
			value := baseVersion
			value.ManifestObjectKey = "other/platform/datasets/dataset-labeled-full/manifests/version-20260830.parquet"
			return value
		}(), binding: baseBinding, request: datasetResolutionRequest(), wantError: "manifest contract is invalid"},
		{name: "zero train samples", version: func() domain.DatasetVersion { value := baseVersion; value.TrainSamples = 0; return value }(), binding: baseBinding, request: datasetResolutionRequest(), wantError: "no training samples"},
		{name: "root pending", version: baseVersion, binding: func() domain.DataMountBinding {
			value := baseBinding
			value.Status = domain.DataMountBindingPending
			return value
		}(), request: datasetResolutionRequest(), wantError: "storage root is unavailable"},
		{name: "binding unavailable", version: baseVersion, binding: baseBinding, request: datasetResolutionRequest(), listErr: errors.New("claim secret"), wantError: "storage root is unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &fakeDatasetManifestCatalog{version: test.version, bindings: []domain.DataMountBinding{test.binding}, getErr: test.getErr, listErr: test.listErr}
			resolver, err := newPrivateDatasetManifestResolver(catalog, "ray-train/platform/datasets")
			if err != nil {
				t.Fatal(err)
			}
			mount, err := resolver.ResolveDatasetManifestMount(context.Background(), test.request)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("mount=%+v err=%v, want %q", mount, err, test.wantError)
			}
			for _, sensitive := range []string{"database host secret", "claim secret", "other/platform"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("resolver error leaked internal detail: %v", err)
				}
			}
		})
	}
}

func TestPrivateDatasetManifestResolverRejectsPrefixOutsideTenantRoot(t *testing.T) {
	_, err := newPrivateDatasetManifestResolver(&fakeDatasetManifestCatalog{}, "private/platform/datasets")
	if err == nil || !strings.Contains(err.Error(), "tenant storage root") {
		t.Fatalf("unexpected error: %v", err)
	}
}
