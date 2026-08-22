package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/domain"
)

func storageAssetRepo(t *testing.T) *GormRepository {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&StorageAssetRecord{}); err != nil {
		t.Fatalf("migrate storage assets: %v", err)
	}
	return repo
}

func testStorageAsset(id, tenantID, ownerUserID, kind string) domain.StorageAsset {
	readOnly := kind != domain.StorageAssetOutput
	return domain.StorageAsset{
		ID:            id,
		TenantID:      tenantID,
		OwnerUserID:   ownerUserID,
		Name:          id,
		Kind:          kind,
		Provider:      domain.StorageProviderTOS,
		ClaimName:     "tos-" + id,
		RootPrefix:    "datasets/" + id + "/",
		ReadOnly:      readOnly,
		BrowseEnabled: true,
	}
}

func TestListStorageAssetsScopesToSharedTenantAndOwner(t *testing.T) {
	repo := storageAssetRepo(t)
	ctx := context.Background()
	assets := []domain.StorageAsset{
		testStorageAsset("shared", "", "", domain.StorageAssetDataset),
		testStorageAsset("tenant", "tenant-a", "", domain.StorageAssetDataset),
		testStorageAsset("mine", "tenant-a", "user-a", domain.StorageAssetDataset),
		testStorageAsset("other-user", "tenant-a", "user-b", domain.StorageAssetDataset),
		testStorageAsset("other-tenant", "tenant-b", "", domain.StorageAssetDataset),
	}
	for _, asset := range assets {
		if err := repo.CreateStorageAsset(ctx, asset); err != nil {
			t.Fatalf("create asset %s: %v", asset.ID, err)
		}
	}

	visible, err := repo.ListStorageAssets(ctx, "tenant-a", "user-a", domain.StorageAssetDataset)
	if err != nil {
		t.Fatalf("list storage assets: %v", err)
	}
	ids := map[string]bool{}
	for _, asset := range visible {
		ids[asset.ID] = true
	}
	for _, expected := range []string{"shared", "tenant", "mine"} {
		if !ids[expected] {
			t.Fatalf("expected %q in visible assets: %#v", expected, ids)
		}
	}
	for _, forbidden := range []string{"other-user", "other-tenant"} {
		if ids[forbidden] {
			t.Fatalf("storage asset %q leaked: %#v", forbidden, ids)
		}
	}
}

func TestGetStorageAssetDoesNotExposeAnotherUsersPrivateAsset(t *testing.T) {
	repo := storageAssetRepo(t)
	asset := testStorageAsset("private", "tenant-a", "user-a", domain.StorageAssetDataset)
	if err := repo.CreateStorageAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetStorageAsset(context.Background(), "tenant-a", "user-b", asset.ID); err != ErrStorageAssetNotFound {
		t.Fatalf("expected scoped not found, got %v", err)
	}
}
