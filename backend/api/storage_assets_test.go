package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

type fakeStorageAssetStore struct {
	assets []domain.StorageAsset
}

func (store *fakeStorageAssetStore) CreateStorageAsset(_ context.Context, asset domain.StorageAsset) error {
	store.assets = append(store.assets, asset)
	return nil
}

func (store *fakeStorageAssetStore) ListStorageAssets(_ context.Context, tenantID, userID, kind string) ([]domain.StorageAsset, error) {
	assets := make([]domain.StorageAsset, 0)
	for _, asset := range store.assets {
		if asset.AllowedFor(tenantID, userID) && (kind == "" || asset.Kind == kind) {
			assets = append(assets, asset)
		}
	}
	return assets, nil
}

func (store *fakeStorageAssetStore) GetStorageAsset(_ context.Context, tenantID, userID, id string) (domain.StorageAsset, error) {
	for _, asset := range store.assets {
		if asset.ID == id && asset.AllowedFor(tenantID, userID) {
			return asset, nil
		}
	}
	return domain.StorageAsset{}, repositories.ErrStorageAssetNotFound
}

func (store *fakeStorageAssetStore) DeleteStorageAsset(_ context.Context, tenantID, id string, superAdmin bool) error {
	for index, asset := range store.assets {
		if asset.ID != id || (!superAdmin && asset.TenantID != tenantID) {
			continue
		}
		store.assets = append(store.assets[:index], store.assets[index+1:]...)
		return nil
	}
	return repositories.ErrStorageAssetNotFound
}

type fakeDirectoryLister struct {
	page   objectstore.DirectoryPage
	err    error
	called bool
	root   string
	path   string
	cursor string
	limit  int
}

func (lister *fakeDirectoryLister) ListDirectories(_ context.Context, rootPrefix, relativePath, cursor string, limit int) (objectstore.DirectoryPage, error) {
	lister.called, lister.root, lister.path, lister.cursor, lister.limit = true, rootPrefix, relativePath, cursor, limit
	return lister.page, lister.err
}

func storageAssetForAPI(id, tenant string) domain.StorageAsset {
	return domain.StorageAsset{
		ID: id, TenantID: tenant, Name: id, Kind: domain.StorageAssetDataset,
		Provider: domain.StorageProviderTOS, ClaimName: "tos-" + id,
		RootPrefix: "datasets/" + id + "/", ReadOnly: true, BrowseEnabled: true,
	}
}

func storageAssetRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterStorageAssetRoutes(router.Group("/api/v1"))
	return router
}

func TestStorageAssetListHidesOtherTenantAssets(t *testing.T) {
	store := &fakeStorageAssetStore{assets: []domain.StorageAsset{
		storageAssetForAPI("shared", ""), storageAssetForAPI("tenant-a", "tenant-a"), storageAssetForAPI("tenant-b", "tenant-b"),
	}}
	handler := NewHandler(&fakeJobRepository{}, Options{StorageAssets: store})
	router := storageAssetRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-assets?kind=dataset", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "tenant-b") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"claimName", "rootPrefix", "datasets/"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("catalogue response leaked infrastructure detail %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestStorageAssetDirectoryRouteRejectsTraversal(t *testing.T) {
	store := &fakeStorageAssetStore{assets: []domain.StorageAsset{storageAssetForAPI("asset-1", "tenant-a")}}
	lister := &fakeDirectoryLister{}
	handler := NewHandler(&fakeJobRepository{}, Options{StorageAssets: store, DirectoryLister: lister})
	router := storageAssetRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-assets/asset-1/directories?path=../secret", nil))
	if response.Code != http.StatusBadRequest || lister.called {
		t.Fatalf("status=%d lister_called=%t body=%s", response.Code, lister.called, response.Body.String())
	}
}

func TestStorageAssetDirectoryRouteReturnsOnlyBrowserContract(t *testing.T) {
	asset := storageAssetForAPI("asset-1", "tenant-a")
	store := &fakeStorageAssetStore{assets: []domain.StorageAsset{asset}}
	lister := &fakeDirectoryLister{page: objectstore.DirectoryPage{Directories: []string{"train", "validation"}, NextCursor: "opaque"}}
	handler := NewHandler(&fakeJobRepository{}, Options{StorageAssets: store, DirectoryLister: lister})
	router := storageAssetRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-assets/asset-1/directories?path=images&cursor=opaque", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "validation") || strings.Contains(response.Body.String(), asset.RootPrefix) || strings.Contains(response.Body.String(), asset.ClaimName) {
		t.Fatalf("unexpected browser response: status=%d body=%s", response.Code, response.Body.String())
	}
	if !lister.called || lister.root != asset.RootPrefix || lister.path != "images" || lister.cursor != "opaque" {
		t.Fatalf("unexpected lister input: %#v", lister)
	}
}

func TestStorageAssetDirectoryRouteMapsUnavailableStoreWithoutDetail(t *testing.T) {
	store := &fakeStorageAssetStore{assets: []domain.StorageAsset{storageAssetForAPI("asset-1", "tenant-a")}}
	lister := &fakeDirectoryLister{err: errors.New("bucket private-bucket rejected secret key")}
	handler := NewHandler(&fakeJobRepository{}, Options{StorageAssets: store, DirectoryLister: lister})
	router := storageAssetRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/storage-assets/asset-1/directories", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private-bucket") {
		t.Fatalf("directory failure leaked storage detail: status=%d body=%s", response.Code, response.Body.String())
	}
}
