package objectstore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type fakeDataEntryClient struct {
	fakeTOSClient
	listRequest  tosArtifactListRequest
	listResponse tosArtifactListResponse
	readRequest  tosArtifactReadRequest
}

func (client *fakeDataEntryClient) ListArtifacts(_ context.Context, request tosArtifactListRequest) (tosArtifactListResponse, error) {
	client.listRequest = request
	return client.listResponse, nil
}

func (client *fakeDataEntryClient) ReadArtifact(_ context.Context, request tosArtifactReadRequest) (tosArtifactReadResponse, error) {
	client.readRequest = request
	return tosArtifactReadResponse{}, ErrNotFound
}

func TestTOSStoreListsDataEntriesWithoutEscapingAuthorizedRoot(t *testing.T) {
	client := &fakeDataEntryClient{listResponse: tosArtifactListResponse{
		Directories: []string{"ray-train/tenants/a/users/u/files/train/", "ray-train/tenants/a/users/u/files/private/"},
		Objects: []tosArtifactObject{
			{Key: "ray-train/tenants/a/users/u/files/README.md", SizeBytes: 9},
			{Key: "ray-train/tenants/a/users/other/files/secret.txt", SizeBytes: 1},
		},
	}}
	store, err := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"}, client)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListDataEntries(context.Background(), "ray-train/tenants/a/users/u/files/", "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if client.listRequest.Prefix != "ray-train/tenants/a/users/u/files/" || client.listRequest.Delimiter != "/" {
		t.Fatalf("unsafe list request: %#v", client.listRequest)
	}
	if !reflect.DeepEqual(page.Entries, []DataEntry{{Name: "private", Type: DataEntryDirectory}, {Name: "train", Type: DataEntryDirectory}, {Name: "README.md", Type: DataEntryFile, SizeBytes: 9}}) {
		t.Fatalf("unexpected scoped entries: %#v", page.Entries)
	}
	if _, err := store.ListDataEntries(context.Background(), "ray-train/tenants/a/users/u/files/", "../other", "", 100); err == nil {
		t.Fatal("cross-user traversal was accepted")
	}
}

func TestTOSStorePresignsOnlyWritableDataPathWithBoundedTTL(t *testing.T) {
	client := &fakeTOSClient{}
	store, err := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"}, client)
	if err != nil {
		t.Fatal(err)
	}
	key := "ray-train/tenants/a/users/u/files/input.csv"
	client.presignResponse = validPresignResponse("https://bucket.tos.example.com/"+key, fullDataUploadHeaders("text/csv", 1234))
	result, err := store.PresignDataPut(context.Background(), "ray-train/tenants/a/users/u/files/", "input.csv", "text/csv", 1234, 15*time.Minute)
	if err != nil || client.lastPresign.Key != key || client.lastPresign.ExpiresSeconds != 900 || result.ContentLength != 1234 {
		t.Fatalf("presign result err=%v request=%#v", err, client.lastPresign)
	}
	if client.lastPresign.Headers["Content-Length"] != "1234" || result.RequiredHeaders["Content-Length"] != "" {
		t.Fatalf("data upload must sign but not expose Content-Length: request=%#v result=%#v", client.lastPresign, result)
	}
	if _, err := store.PresignDataPut(context.Background(), "ray-train/tenants/a/users/u/files/", "../other/secret", "text/plain", 1, 15*time.Minute); err == nil {
		t.Fatal("cross-user upload was accepted")
	}
	if _, err := store.PresignDataPut(context.Background(), "ray-train/tenants/a/users/u/files/", "too-large.bin", "application/octet-stream", maxDataUploadBytes+1, 15*time.Minute); err == nil {
		t.Fatal("oversized data upload was accepted")
	}
	zeroKey := "ray-train/tenants/a/users/u/files/mmdet3d/__init__.py"
	client.presignResponse = validPresignResponse("https://bucket.tos.example.com/"+zeroKey, fullDataUploadHeaders("text/x-python", 0))
	zero, err := store.PresignDataPut(context.Background(), "ray-train/tenants/a/users/u/files/", "mmdet3d/__init__.py", "text/x-python", 0, 15*time.Minute)
	if err != nil || zero.ContentLength != 0 || client.lastPresign.Headers["Content-Length"] != "0" {
		t.Fatalf("zero-byte source file presign err=%v request=%#v result=%#v", err, client.lastPresign, zero)
	}
	if _, err := store.PresignDataPut(context.Background(), "ray-train/tenants/a/users/u/files/", "negative.bin", "application/octet-stream", -1, 15*time.Minute); err == nil {
		t.Fatal("negative data upload size was accepted")
	}
}
