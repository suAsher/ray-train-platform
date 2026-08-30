package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

const governedTenantStorageRoot = "ray-train"

type privateDatasetManifestCatalog interface {
	GetDatasetVersion(context.Context, string, bool, string, string) (domain.DatasetVersion, error)
	ListDataBindings(context.Context, string, string) ([]domain.DataMountBinding, error)
}

// privateDatasetManifestResolver joins two platform-only inventories: an
// immutable READY DatasetVersion and the tenant-local PVC rooted at
// ray-train/. Neither the internal object key nor the PVC subPath is accepted
// from a training request.
type privateDatasetManifestResolver struct {
	catalog        privateDatasetManifestCatalog
	internalPrefix string
	relativePrefix string
}

func newPrivateDatasetManifestResolver(catalog privateDatasetManifestCatalog, rawPrefix string) (*privateDatasetManifestResolver, error) {
	if catalog == nil {
		return nil, fmt.Errorf("dataset manifest catalog is required")
	}
	prefix, err := domain.NormalizeDatasetInternalPrefix(rawPrefix)
	if err != nil {
		return nil, fmt.Errorf("dataset internal prefix is invalid")
	}
	rootPrefix := governedTenantStorageRoot + "/"
	if !strings.HasPrefix(prefix, rootPrefix) || prefix == strings.TrimSuffix(rootPrefix, "/") {
		return nil, fmt.Errorf("dataset internal prefix must remain below the tenant storage root")
	}
	relative := strings.TrimPrefix(prefix, rootPrefix)
	if normalized, err := domain.NormalizeStorageRelativePath(relative); err != nil || normalized != relative {
		return nil, fmt.Errorf("dataset internal prefix must remain below the tenant storage root")
	}
	return &privateDatasetManifestResolver{
		catalog: catalog, internalPrefix: prefix, relativePrefix: relative,
	}, nil
}

func (resolver *privateDatasetManifestResolver) ResolveDatasetManifestMount(
	ctx context.Context,
	request k8s.DatasetManifestResolutionRequest,
) (k8s.DatasetManifestMount, error) {
	if resolver == nil || resolver.catalog == nil {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset manifest resolver is unavailable")
	}
	if strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.TenantID) != request.TenantID {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset tenant is invalid")
	}
	provenance := domain.DatasetProvenance{
		DatasetID: request.DatasetID, DatasetVersionID: request.DatasetVersionID,
		ManifestSHA256: request.ManifestSHA256, DataMode: domain.DataModeStreaming,
		CachePolicy: domain.DatasetCachePolicyAuto,
	}
	if err := provenance.Validate(); err != nil {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset provenance is invalid")
	}

	version, err := resolver.catalog.GetDatasetVersion(
		ctx, request.TenantID, false, request.DatasetID, request.DatasetVersionID,
	)
	if err != nil {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset version is unavailable")
	}
	if version.State != domain.DatasetVersionReady {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset version is not ready")
	}
	if err := version.ValidateWithInternalPrefix(resolver.internalPrefix); err != nil {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset manifest contract is invalid")
	}
	if version.DatasetID != request.DatasetID || version.ID != request.DatasetVersionID || version.ManifestSHA256 != request.ManifestSHA256 {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset provenance does not match the ready version")
	}
	if version.TrainSamples <= 0 {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset version has no training samples")
	}

	bindings, err := resolver.catalog.ListDataBindings(ctx, request.TenantID, "")
	if err != nil {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset storage root is unavailable")
	}
	binding, ok := readyTenantStorageRoot(bindings, request.TenantID)
	if !ok {
		return k8s.DatasetManifestMount{}, fmt.Errorf("dataset storage root is unavailable")
	}
	datasetSubPath := path.Join(resolver.relativePrefix, request.DatasetID)
	return k8s.DatasetManifestMount{
		DatasetID: request.DatasetID, DatasetVersionID: request.DatasetVersionID,
		ManifestSHA256: request.ManifestSHA256, TrainSamples: version.TrainSamples,
		ClaimName: binding.ClaimName, DatasetRootSubPath: datasetSubPath,
	}, nil
}

func readyTenantStorageRoot(bindings []domain.DataMountBinding, tenantID string) (domain.DataMountBinding, bool) {
	for _, binding := range bindings {
		if binding.TenantID != tenantID || binding.Scope != domain.DataMountScopeTenant ||
			binding.SpaceID != domain.DataSpaceTenantStorageRoot || binding.ReadOnly ||
			binding.Status != domain.DataMountBindingReady ||
			strings.TrimSuffix(binding.RootPrefix, "/") != governedTenantStorageRoot {
			continue
		}
		if err := binding.Validate(); err != nil {
			continue
		}
		return binding, true
	}
	return domain.DataMountBinding{}, false
}
