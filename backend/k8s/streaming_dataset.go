package k8s

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"

	"ray-train-platform-backend/domain"
)

const (
	datasetRootVolumeName = "platform-dataset-root"

	// DatasetRootContainerPath is the stable parent consumed by the
	// platform-owned managed driver. Each workload mounts only its selected
	// dataset below this directory; it never receives a bucket URI or storage
	// credential.
	DatasetRootContainerPath = "/mnt/data/.platform/datasets"

	defaultStreamingDatasetPrefetchBatches = int64(2)
	defaultStreamingDatasetShuffleSeed     = uint64(0)
)

// DatasetManifestResolutionRequest is the immutable lookup key passed from
// the reconciler to a private catalogue adapter. TrainingJob intentionally
// does not persist the manifest object key, so the renderer must never derive
// one from user-visible dataset fields.
type DatasetManifestResolutionRequest struct {
	TenantID         string
	DatasetID        string
	DatasetVersionID string
	ManifestSHA256   string
}

// DatasetManifestMount is a control-plane-resolved, read-only PVC directory
// mount. Despite the historical type name, DatasetRootSubPath identifies the
// selected dataset root, not a single manifest object. This lets the managed
// driver read both the pinned manifest and only that dataset's content-
// addressed shards.
type DatasetManifestMount struct {
	DatasetID          string
	DatasetVersionID   string
	ManifestSHA256     string
	TrainSamples       int64
	ClaimName          string
	DatasetRootSubPath string
}

// DatasetManifestResolver is implemented by the private dataset catalogue
// adapter. It must load the exact READY DatasetVersion, validate its internal
// prefix and manifest object key, and return a platform-controlled PVC plus
// the confined dataset directory relative to that PVC's governed root. The
// result is never accepted from a submission request.
type DatasetManifestResolver interface {
	ResolveDatasetManifestMount(context.Context, DatasetManifestResolutionRequest) (DatasetManifestMount, error)
}

func validateStreamingDatasetManifest(job domain.TrainingJob, mount *DatasetManifestMount) error {
	provenance := job.DatasetProvenance
	if err := provenance.Validate(); err != nil {
		return fmt.Errorf("immutable dataset provenance: %w", err)
	}
	if provenance.IsZero() {
		return fmt.Errorf("immutable dataset provenance is required")
	}
	if job.Spec.DatasetRef.Dataset != provenance.DatasetID || job.Spec.DatasetRef.Version != provenance.DatasetVersionID ||
		job.Spec.DataMode != provenance.DataMode || job.Spec.CachePolicy != provenance.CachePolicy {
		return fmt.Errorf("streaming job spec does not match immutable dataset provenance")
	}
	if mount == nil {
		return fmt.Errorf("resolved dataset root is required for streaming")
	}
	if err := mount.validate(provenance); err != nil {
		return fmt.Errorf("resolved dataset root: %w", err)
	}
	return nil
}

func (mount DatasetManifestMount) validate(provenance domain.DatasetProvenance) error {
	if mount.DatasetID != provenance.DatasetID || mount.DatasetVersionID != provenance.DatasetVersionID || mount.ManifestSHA256 != provenance.ManifestSHA256 {
		return fmt.Errorf("mount identity does not match immutable provenance")
	}
	if !isDNSSubdomain(strings.TrimSpace(mount.ClaimName)) || strings.TrimSpace(mount.ClaimName) != mount.ClaimName {
		return fmt.Errorf("PVC claim name must be a canonical Kubernetes name")
	}
	if mount.TrainSamples <= 0 {
		return fmt.Errorf("training sample count must be positive")
	}
	if err := validateDatasetRootSubPath(mount.DatasetRootSubPath, provenance.DatasetID); err != nil {
		return err
	}
	return nil
}

func validateDatasetRootSubPath(value, datasetID string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 1024 {
		return fmt.Errorf("dataset root PVC subPath must be a non-empty canonical relative path")
	}
	if strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." ||
		strings.Contains(value, "\\") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("dataset root PVC subPath must be a clean relative path")
	}
	// Percent-encoded separators or traversal have ambiguous semantics across
	// object storage, FUSE and URL tooling. Publisher-generated manifest keys do
	// not require percent escapes, so fail closed rather than decode repeatedly.
	if strings.Contains(value, "%") {
		return fmt.Errorf("dataset root PVC subPath must not contain encoded path components")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("dataset root PVC subPath must not contain traversal components")
		}
	}
	marker := "/" + datasetID
	if !strings.HasSuffix(value, marker) || strings.TrimSuffix(value, marker) == "" {
		return fmt.Errorf("dataset root PVC subPath does not end at the selected dataset")
	}
	return nil
}

func appendStreamingDatasetRoot(
	volumeMounts []any,
	volumes []any,
	environment []any,
	mount DatasetManifestMount,
	cachePolicy domain.DatasetCachePolicy,
) ([]any, []any, []any) {
	volumeMounts = append(volumeMounts, map[string]any{
		"name": datasetRootVolumeName, "mountPath": datasetContainerPath(mount.DatasetID),
		"subPath": mount.DatasetRootSubPath, "readOnly": true,
	})
	volumes = append(volumes, map[string]any{
		"name": datasetRootVolumeName,
		"persistentVolumeClaim": map[string]any{
			"claimName": mount.ClaimName, "readOnly": true,
		},
	})
	environment = append(environment, streamingDatasetEnvironmentEntries(mount, cachePolicy)...)
	return volumeMounts, volumes, environment
}

func streamingDatasetEnvironmentEntries(mount DatasetManifestMount, cachePolicy domain.DatasetCachePolicy) []any {
	return []any{
		map[string]any{"name": "PLATFORM_DATASET_ID", "value": mount.DatasetID},
		map[string]any{"name": "PLATFORM_DATASET_VERSION_ID", "value": mount.DatasetVersionID},
		map[string]any{"name": "PLATFORM_DATASET_MANIFEST_SHA256", "value": mount.ManifestSHA256},
		map[string]any{"name": "PLATFORM_DATASET_MANIFEST_PATH", "value": datasetManifestContainerPath(mount.DatasetID, mount.DatasetVersionID)},
		map[string]any{"name": "PLATFORM_DATASET_ROOT", "value": DatasetRootContainerPath},
		map[string]any{"name": "PLATFORM_DATASET_TRAIN_SAMPLES", "value": strconv.FormatInt(mount.TrainSamples, 10)},
		map[string]any{"name": "PLATFORM_DATASET_CACHE_POLICY", "value": string(cachePolicy)},
		map[string]any{"name": "RAYTRAIN_DATASET_PREFETCH_BATCHES", "value": strconv.FormatInt(defaultStreamingDatasetPrefetchBatches, 10)},
		map[string]any{"name": "RAYTRAIN_DATASET_SHUFFLE_SEED", "value": strconv.FormatUint(defaultStreamingDatasetShuffleSeed, 10)},
	}
}

func streamingDatasetRuntimeEnvironmentYAML(provenance domain.DatasetProvenance, mount DatasetManifestMount) string {
	return "  PLATFORM_DATASET_ID: " + strconv.Quote(provenance.DatasetID) + "\n" +
		"  PLATFORM_DATASET_VERSION_ID: " + strconv.Quote(provenance.DatasetVersionID) + "\n" +
		"  PLATFORM_DATASET_MANIFEST_SHA256: " + strconv.Quote(provenance.ManifestSHA256) + "\n" +
		"  PLATFORM_DATASET_MANIFEST_PATH: " + strconv.Quote(datasetManifestContainerPath(provenance.DatasetID, provenance.DatasetVersionID)) + "\n" +
		"  PLATFORM_DATASET_ROOT: " + strconv.Quote(DatasetRootContainerPath) + "\n" +
		"  PLATFORM_DATASET_TRAIN_SAMPLES: " + strconv.Quote(strconv.FormatInt(mount.TrainSamples, 10)) + "\n" +
		"  PLATFORM_DATASET_CACHE_POLICY: " + strconv.Quote(string(provenance.CachePolicy)) + "\n" +
		"  RAYTRAIN_DATASET_PREFETCH_BATCHES: " + strconv.Quote(strconv.FormatInt(defaultStreamingDatasetPrefetchBatches, 10)) + "\n" +
		"  RAYTRAIN_DATASET_SHUFFLE_SEED: " + strconv.Quote(strconv.FormatUint(defaultStreamingDatasetShuffleSeed, 10)) + "\n"
}

func datasetContainerPath(datasetID string) string {
	return path.Join(DatasetRootContainerPath, datasetID)
}

func datasetManifestContainerPath(datasetID, versionID string) string {
	return path.Join(datasetContainerPath(datasetID), "manifests", versionID+".parquet")
}
