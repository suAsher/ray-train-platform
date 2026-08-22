package domain

import "testing"

func validDatasetAsset() StorageAsset {
	return StorageAsset{
		ID:            "asset-dataset",
		TenantID:      "tenant-a",
		Name:          "vision-dataset",
		Kind:          StorageAssetDataset,
		Provider:      StorageProviderTOS,
		ClaimName:     "tos-vision-dataset-ro",
		RootPrefix:    "datasets/tenant-a/vision/",
		ReadOnly:      true,
		BrowseEnabled: true,
	}
}

func TestStorageAssetValidateRejectsUnsafePrefixAndWritableInput(t *testing.T) {
	asset := validDatasetAsset()
	asset.RootPrefix = "datasets/tenant-a/../private"
	if err := asset.Validate(); err == nil {
		t.Fatal("unsafe asset was accepted")
	}

	asset = validDatasetAsset()
	asset.ReadOnly = false
	if err := asset.Validate(); err == nil {
		t.Fatal("writable dataset asset was accepted")
	}
}

func TestResolveStorageSelectionKeepsPathInsideAssetRoot(t *testing.T) {
	asset := validDatasetAsset()
	mount, err := asset.Resolve("images/v1")
	if err != nil {
		t.Fatalf("resolve storage selection: %v", err)
	}
	if mount.RelativePath != "images/v1" || mount.MountPath != StorageMountDataset {
		t.Fatalf("unexpected resolved mount: %#v", mount)
	}
	if _, err := asset.Resolve("../secret"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := asset.Resolve("/absolute"); err == nil {
		t.Fatal("absolute path accepted")
	}
}

func TestStorageAssetVisibilityUsesSharedTenantAndOwnerScopes(t *testing.T) {
	shared := validDatasetAsset()
	shared.TenantID = ""
	if !shared.AllowedFor("tenant-b", "user-b") {
		t.Fatal("shared asset was not visible")
	}

	private := validDatasetAsset()
	private.OwnerUserID = "user-a"
	if !private.AllowedFor("tenant-a", "user-a") {
		t.Fatal("owner could not see private asset")
	}
	if private.AllowedFor("tenant-a", "user-b") {
		t.Fatal("another user could see private asset")
	}
	if private.AllowedFor("tenant-b", "user-a") {
		t.Fatal("another tenant could see private asset")
	}
}
