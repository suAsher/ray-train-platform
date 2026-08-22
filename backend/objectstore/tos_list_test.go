package objectstore

import (
	"context"
	"reflect"
	"testing"
)

type fakeDirectoryClient struct {
	fakeTOSClient
	request  tosDirectoryListRequest
	response tosDirectoryListResponse
	err      error
}

type fakeDataSpaceDirectoryClient struct {
	fakeTOSClient
	keys []string
	err  error
}

func (client *fakeDataSpaceDirectoryClient) PutDirectoryMarker(_ context.Context, bucket, key string) error {
	if client.err != nil {
		return client.err
	}
	if bucket == "" {
		return ErrUnavailable
	}
	client.keys = append(client.keys, key)
	return nil
}

func (client *fakeDirectoryClient) ListDirectories(_ context.Context, request tosDirectoryListRequest) (tosDirectoryListResponse, error) {
	client.request = request
	return client.response, client.err
}

func TestTOSStoreListDirectoriesUsesDelimiterAndNeverEscapesRoot(t *testing.T) {
	client := &fakeDirectoryClient{response: tosDirectoryListResponse{
		Directories:           []string{"datasets/local/a/images/", "datasets/local/b/private/"},
		NextContinuationToken: "opaque-next-token",
	}}
	store, err := newTOSStoreWithClient(TOSConfig{
		Endpoint: "https://tos.example.com", Region: "cn-test", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk",
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.ListDirectories(context.Background(), "datasets/local/a/", "", "", 50)
	if err != nil {
		t.Fatalf("list directories: %v", err)
	}
	if !reflect.DeepEqual(page.Directories, []string{"images"}) || page.NextCursor != "opaque-next-token" {
		t.Fatalf("unexpected page: %#v", page)
	}
	if client.request.Bucket != "private-bucket" || client.request.Prefix != "datasets/local/a/" || client.request.Delimiter != "/" || client.request.MaxKeys != 50 {
		t.Fatalf("unsafe TOS list request: %#v", client.request)
	}
	if _, err := store.ListDirectories(context.Background(), "datasets/local/a/../", "", "", 50); err == nil {
		t.Fatal("unsafe root prefix was accepted")
	}
	if _, err := store.ListDirectories(context.Background(), "datasets/local/a/", "../private", "", 50); err == nil {
		t.Fatal("traversal relative path was accepted")
	}
}

func TestTOSStoreListDirectoriesClampsPageSizeAndDoesNotReturnObjectKeys(t *testing.T) {
	client := &fakeDirectoryClient{response: tosDirectoryListResponse{
		Directories: []string{
			"datasets/local/a/first/",
			"datasets/local/a/second/",
			"datasets/local/a/file.txt",
		},
	}}
	store, err := newTOSStoreWithClient(TOSConfig{
		Endpoint: "https://tos.example.com", Region: "cn-test", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk",
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.ListDirectories(context.Background(), "datasets/local/a/", "", "token", 1000)
	if err != nil {
		t.Fatalf("list directories: %v", err)
	}
	if !reflect.DeepEqual(page.Directories, []string{"first", "second"}) {
		t.Fatalf("directory response leaked an object or full prefix: %#v", page)
	}
	if client.request.MaxKeys != maxDirectoryPageSize || client.request.ContinuationToken != "token" {
		t.Fatalf("unexpected request bounds: %#v", client.request)
	}
}

func TestTOSStoreInitializesOnlyReservedPersonalDataDirectories(t *testing.T) {
	client := &fakeDataSpaceDirectoryClient{}
	store, err := newTOSStoreWithClient(TOSConfig{
		Endpoint: "https://tos.example.com", Region: "cn-test", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk",
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	root := "ray-train/tenants/tenant-a/users/user-a/"
	if err := store.EnsurePersonalDataDirectories(context.Background(), root); err != nil {
		t.Fatalf("initialize personal data directories: %v", err)
	}
	want := []string{
		"ray-train/tenants/tenant-a/users/user-a/workspace/.ray-train-keep",
		"ray-train/tenants/tenant-a/users/user-a/files/.ray-train-keep",
		"ray-train/tenants/tenant-a/users/user-a/runs/.ray-train-keep",
		"ray-train/tenants/tenant-a/users/user-a/snapshots/.ray-train-keep",
	}
	if !reflect.DeepEqual(client.keys, want) {
		t.Fatalf("marker keys=%#v want=%#v", client.keys, want)
	}
	if err := store.EnsurePersonalDataDirectories(context.Background(), root+"../other/"); err == nil {
		t.Fatal("unsafe personal data root was accepted")
	}
}

func TestTOSStoreInitializesOnlyTheGovernedSharedRootMarker(t *testing.T) {
	client := &fakeDataSpaceDirectoryClient{}
	store, err := newTOSStoreWithClient(TOSConfig{
		Endpoint: "https://tos.example.com", Region: "cn-test", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureDataDirectory(context.Background(), "ray-train/tenants/tenant-a/shared/"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.keys, []string{"ray-train/tenants/tenant-a/shared/.ray-train-keep"}) {
		t.Fatalf("unexpected shared marker keys: %#v", client.keys)
	}
	if err := store.EnsureDataDirectory(context.Background(), "ray-train/tenants/tenant-a/shared/../private/"); err == nil {
		t.Fatal("unsafe shared root was accepted")
	}
}
