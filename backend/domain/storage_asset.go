package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	StorageAssetDataset    = "dataset"
	StorageAssetCheckpoint = "checkpoint"
	StorageAssetOutput     = "output"

	StorageProviderTOS = "tos"
	StorageProviderIDC = "idc"

	StorageMountDataset    = "/mnt/data/dataset"
	StorageMountCheckpoint = "/mnt/data/checkpoint"
	StorageMountOutput     = "/mnt/data/output"
)

// StorageAsset is an operator-approved storage root. Its claim must already
// exist in the tenant namespace; the platform never derives a PVC or TOS path
// from a user-supplied value.
type StorageAsset struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenantId,omitempty"`
	OwnerUserID   string    `json:"ownerUserId,omitempty"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Kind          string    `json:"kind"`
	Provider      string    `json:"provider"`
	ClaimName     string    `json:"claimName,omitempty"`
	RootPrefix    string    `json:"-"`
	ReadOnly      bool      `json:"readOnly"`
	BrowseEnabled bool      `json:"browseEnabled"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// StorageSelection is the only storage location supplied by a training
// request. RelativePath is always relative to the asset root, never a TOS URI
// or a filesystem path.
type StorageSelection struct {
	AssetID      string `json:"assetId,omitempty"`
	RelativePath string `json:"relativePath,omitempty"`
}

// ResolvedStorageMount is server-generated submission state. It is persisted
// in a job's spec so the renderer does not need to trust or re-resolve user
// input after the request has been authorized.
type ResolvedStorageMount struct {
	AssetID      string `json:"assetId"`
	ClaimName    string `json:"claimName"`
	RelativePath string `json:"relativePath,omitempty"`
	MountPath    string `json:"mountPath"`
	ReadOnly     bool   `json:"readOnly"`
}

type ResolvedStorageMounts struct {
	Dataset    *ResolvedStorageMount `json:"dataset,omitempty"`
	Checkpoint *ResolvedStorageMount `json:"checkpoint,omitempty"`
	Output     *ResolvedStorageMount `json:"output,omitempty"`
}

func (mounts ResolvedStorageMounts) Validate() error {
	checks := []struct {
		name      string
		mount     *ResolvedStorageMount
		mountPath string
		readOnly  bool
	}{
		{name: "dataset", mount: mounts.Dataset, mountPath: StorageMountDataset, readOnly: true},
		{name: "checkpoint", mount: mounts.Checkpoint, mountPath: StorageMountCheckpoint, readOnly: true},
		{name: "output", mount: mounts.Output, mountPath: StorageMountOutput, readOnly: false},
	}
	for _, check := range checks {
		if check.mount == nil {
			continue
		}
		if strings.TrimSpace(check.mount.AssetID) == "" || strings.TrimSpace(check.mount.ClaimName) == "" || !dnsLabel.MatchString(check.mount.ClaimName) {
			return fmt.Errorf("resolved %s storage mount is invalid", check.name)
		}
		if check.mount.MountPath != check.mountPath || check.mount.ReadOnly != check.readOnly {
			return fmt.Errorf("resolved %s storage mount has an invalid mount contract", check.name)
		}
		if _, err := NormalizeStorageRelativePath(check.mount.RelativePath); err != nil {
			return fmt.Errorf("resolved %s storage mount path: %w", check.name, err)
		}
	}
	return nil
}

func ValidateStorageAssetKind(kind string) error {
	switch kind {
	case StorageAssetDataset, StorageAssetCheckpoint, StorageAssetOutput:
		return nil
	default:
		return fmt.Errorf("storage asset kind must be %q, %q, or %q", StorageAssetDataset, StorageAssetCheckpoint, StorageAssetOutput)
	}
}

func ValidateStorageProvider(provider string) error {
	switch provider {
	case StorageProviderTOS, StorageProviderIDC:
		return nil
	default:
		return fmt.Errorf("storage provider must be %q or %q", StorageProviderTOS, StorageProviderIDC)
	}
}

func (asset StorageAsset) Validate() error {
	if strings.TrimSpace(asset.ID) == "" {
		return fmt.Errorf("storage asset id is required")
	}
	if strings.TrimSpace(asset.Name) == "" {
		return fmt.Errorf("storage asset name is required")
	}
	if err := ValidateStorageAssetKind(asset.Kind); err != nil {
		return err
	}
	if err := ValidateStorageProvider(asset.Provider); err != nil {
		return err
	}
	if strings.TrimSpace(asset.ClaimName) == "" || !dnsLabel.MatchString(asset.ClaimName) {
		return fmt.Errorf("storage asset claim name must be a lowercase DNS label")
	}
	if asset.TenantID == "" && asset.OwnerUserID != "" {
		return fmt.Errorf("a user-owned storage asset requires a tenant")
	}
	if asset.Kind == StorageAssetOutput && asset.ReadOnly {
		return fmt.Errorf("output storage assets must be writable")
	}
	if asset.Kind != StorageAssetOutput && !asset.ReadOnly {
		return fmt.Errorf("dataset and checkpoint storage assets must be read-only")
	}
	if asset.Provider == StorageProviderTOS {
		if _, err := normalizeStorageRoot(asset.RootPrefix); err != nil {
			return fmt.Errorf("storage asset root prefix: %w", err)
		}
	}
	if asset.Provider == StorageProviderIDC {
		if strings.TrimSpace(asset.RootPrefix) != "" {
			return fmt.Errorf("IDC storage assets must not expose a storage root prefix")
		}
		if asset.BrowseEnabled {
			return fmt.Errorf("IDC storage assets do not support directory browsing")
		}
	}
	return nil
}

// Canonical returns a copy with a canonical TOS prefix. It does not mutate its
// receiver, avoiding accidental changes to values still held by an HTTP request
// or repository caller.
func (asset StorageAsset) Canonical() (StorageAsset, error) {
	if asset.Provider == StorageProviderTOS {
		prefix, err := normalizeStorageRoot(asset.RootPrefix)
		if err != nil {
			return StorageAsset{}, err
		}
		asset.RootPrefix = prefix
	}
	return asset, nil
}

// AllowedFor expresses the catalogue visibility rule: a shared asset is
// visible to everyone, a tenant asset to that tenant, and a user asset only to
// its owning user within that tenant.
func (asset StorageAsset) AllowedFor(tenantID, userID string) bool {
	if asset.TenantID == "" {
		return asset.OwnerUserID == ""
	}
	if asset.TenantID != tenantID {
		return false
	}
	return asset.OwnerUserID == "" || asset.OwnerUserID == userID
}

func (asset StorageAsset) Resolve(relativePath string) (ResolvedStorageMount, error) {
	if err := asset.Validate(); err != nil {
		return ResolvedStorageMount{}, err
	}
	path, err := NormalizeStorageRelativePath(relativePath)
	if err != nil {
		return ResolvedStorageMount{}, err
	}
	mountPath, err := storageMountPath(asset.Kind)
	if err != nil {
		return ResolvedStorageMount{}, err
	}
	return ResolvedStorageMount{
		AssetID: asset.ID, ClaimName: asset.ClaimName, RelativePath: path,
		MountPath: mountPath, ReadOnly: asset.ReadOnly,
	}, nil
}

func storageMountPath(kind string) (string, error) {
	switch kind {
	case StorageAssetDataset:
		return StorageMountDataset, nil
	case StorageAssetCheckpoint:
		return StorageMountCheckpoint, nil
	case StorageAssetOutput:
		return StorageMountOutput, nil
	default:
		return "", ValidateStorageAssetKind(kind)
	}
}

// NormalizeStorageRelativePath accepts a directory inside an already
// authorized asset. It rejects URI-like and absolute paths before any object
// store or filesystem operation can be attempted.
func NormalizeStorageRelativePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "://") {
		return "", fmt.Errorf("storage path must be relative")
	}
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return "", fmt.Errorf("storage path must not be root slash")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsRune(segment, '\x00') {
			return "", fmt.Errorf("storage path contains an unsafe segment")
		}
	}
	return value, nil
}

func normalizeStorageRoot(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("storage root prefix is required")
	}
	path, err := NormalizeStorageRelativePath(strings.TrimSuffix(value, "/"))
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("storage root prefix is required")
	}
	return path + "/", nil
}
