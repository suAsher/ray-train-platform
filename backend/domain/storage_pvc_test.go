package domain

import "testing"

func TestPVCAssetPreservesScopeAndSafeMountContract(t *testing.T) {
	asset := StorageAsset{ID: "pvc-data", TenantID: "team-a", OwnerUserID: "alice", Name: "dataset", Kind: StorageAssetDataset, Provider: StorageProviderPVC, ClaimName: "training-data", ReadOnly: true}
	if !asset.AllowedFor("team-a", "alice") || asset.AllowedFor("team-b", "alice") || asset.AllowedFor("team-a", "bob") {
		t.Fatal("PVC asset escaped its owner/tenant scope")
	}
	mount, err := asset.Resolve("dataset/train")
	if err != nil || mount.ClaimName != "training-data" || !mount.ReadOnly || mount.MountPath != StorageMountDataset {
		t.Fatalf("PVC mount = %#v, error = %v", mount, err)
	}
	for _, unsafe := range []string{"../private", "/etc", "a/../../b", "tos://bucket/path", "a\\b"} {
		if _, err := asset.Resolve(unsafe); err == nil {
			t.Fatalf("unsafe path accepted: %q", unsafe)
		}
	}
	for _, change := range []func(*StorageAsset){
		func(a *StorageAsset) { a.RootPrefix = "private/" },
		func(a *StorageAsset) { a.BrowseEnabled = true },
		func(a *StorageAsset) { a.ReadOnly = false },
		func(a *StorageAsset) { a.ClaimName = "../other-claim" },
	} {
		invalid := asset
		change(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("unsafe PVC asset accepted: %#v", invalid)
		}
	}
}
